// Package provider adapts model HTTP protocols to normalized runtime events.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/iamnalinor/qual/internal/diagnostic"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/port"
)

// Credential is the narrow authentication input; it is never logged.
type (
	Credential struct{ AccessToken, AccountID, Kind string }
	// TokenSource obtains an authorized token, refreshing it if necessary.
	TokenSource interface {
		Token(context.Context) (Credential, error)
	}
	// TokenSourceFunc adapts a credential callback.
	TokenSourceFunc func(context.Context) (Credential, error)
)

// Token invokes the selected credential callback.
func (f TokenSourceFunc) Token(ctx context.Context) (Credential, error) { return f(ctx) }

// Config selects one explicit protocol and billing route. Capabilities overrides
// provide verified capabilities for private/self-hosted model deployments.
type Config struct {
	Kind, BaseURL, APIKey, Model string
	HTTPClient                   *http.Client
	TokenSource                  TokenSource
	Capabilities                 []domain.ModelInfo
}
type adapter struct {
	config Config
	client *http.Client
}

// New constructs an adapter. It never selects another credential or billing route.
func New(c Config) (port.Provider, error) {
	defaults := map[string]string{"openai": "https://api.openai.com/v1", "chatgpt": "https://api.openai.com/v1", "codex": "https://chatgpt.com/backend-api/codex", "anthropic": "https://api.anthropic.com/v1", "gemini": "https://generativelanguage.googleapis.com/v1beta", "openrouter": "https://openrouter.ai/api/v1", "openai-compatible": ""}
	endpoint, ok := defaults[c.Kind]
	if !ok {
		return nil, problem(domain.ErrInvalidConfig, "unknown provider kind")
	}
	if c.BaseURL == "" {
		c.BaseURL = endpoint
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || !validBaseURL(u) {
		return nil, problem(domain.ErrInvalidConfig, "invalid provider base URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, problem(domain.ErrInvalidConfig, "provider base URL must not contain query or fragment")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	// Redirects could leak credentials to another origin or repeat a mutation.
	copyClient := *client
	if copyClient.Timeout <= 0 {
		copyClient.Timeout = 5 * time.Minute
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &adapter{config: c, client: &copyClient}, nil
}

func problem(code domain.ErrorCode, message string) *domain.Error {
	return &domain.Error{Code: code, Message: message}
}

func (p *adapter) credential(ctx context.Context) (Credential, error) {
	if p.config.Kind == "chatgpt" || p.config.Kind == "codex" {
		if p.config.TokenSource == nil {
			return Credential{}, problem(domain.ErrAuth, "selected subscription route requires OAuth login")
		}
		token, err := p.config.TokenSource.Token(ctx)
		if err != nil {
			return Credential{}, err
		}
		if token.Kind != "" && token.Kind != p.config.Kind {
			return Credential{}, problem(domain.ErrAuth, "credential belongs to a different billing route")
		}
		if token.AccessToken == "" {
			return Credential{}, problem(domain.ErrAuth, "empty OAuth credential")
		}
		return token, nil
	}
	if p.config.APIKey == "" && p.config.Kind != "openai-compatible" {
		return Credential{}, problem(domain.ErrAuth, "selected provider requires an API key")
	}
	return Credential{AccessToken: p.config.APIKey}, nil
}

func (p *adapter) Run(ctx context.Context, input domain.ModelRequest, emit func(domain.ModelEvent) error) error {
	if emit == nil {
		return problem(domain.ErrInvalidConfig, "event callback is required")
	}
	input.Model = p.modelID(input.Model)
	if input.Model == "" {
		return problem(domain.ErrInvalidConfig, "model is required")
	}
	if err := p.validate(input); err != nil {
		return err
	}
	token, err := p.credential(ctx)
	if err != nil {
		return err
	}
	req, err := p.httpRequest(ctx, input, token)
	if err != nil {
		return err
	}
	response, err := p.client.Do(req)
	if err != nil {
		return transportError(ctx, err, token.AccessToken, token.AccountID, p.config.APIKey)
	}
	defer response.Body.Close() //nolint:errcheck // The response read result determines success; closing a read-only body cannot recover a request.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return statusError(response, token.AccessToken, token.AccountID, p.config.APIKey)
	}
	stream := newStream(p.config.Kind, emit)
	err = readSSE(ctx, response.Body, stream.consume, token.AccessToken, token.AccountID, p.config.APIKey)
	if err != nil {
		return err
	}
	if !stream.terminal {
		return problem(domain.ErrIncomplete, "provider stream ended without successful terminal event")
	}
	return stream.finish()
}

func (p *adapter) modelID(id string) string {
	if id != "" {
		return id
	}
	return p.config.Model
}

func (p *adapter) httpRequest(ctx context.Context, input domain.ModelRequest, token Credential) (*http.Request, error) {
	path, body, err := p.request(input)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode model request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, &domain.Error{Code: domain.ErrInvalidConfig, Message: "build provider request", Cause: diagnostic.Cause(err, token.AccessToken, token.AccountID, p.config.APIKey)}
	}
	p.headers(req, token)
	if p.config.Kind == "chatgpt" || p.config.Kind == "codex" {
		req.Header.Set("session-id", string(input.RunID))
		req.Header.Set("thread-id", string(input.RunID))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	return req, nil
}

func transportError(ctx context.Context, err error, secrets ...string) error {
	if ctx.Err() != nil {
		return &domain.Error{Code: domain.ErrCancelled, Message: "provider request cancelled", Cause: ctx.Err()}
	}
	// Inspect the original cause before redaction so timeout classification survives.
	var netError interface{ Timeout() bool }
	retry := errors.As(err, &netError) && netError.Timeout()
	return &domain.Error{Code: "provider_transport", Message: "provider transport failed", Retryable: retry, Cause: diagnostic.Cause(err, secrets...)}
}

func (p *adapter) headers(req *http.Request, token Credential) {
	if token.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}
	switch p.config.Kind {
	case "anthropic":
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", token.AccessToken)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		req.Header.Del("Authorization")
		req.Header.Set("x-goog-api-key", token.AccessToken)
	case "codex":
		req.Header.Set("ChatGPT-Account-Id", token.AccountID)
		req.Header.Set("originator", "qual")
	}
}

func (p *adapter) request(r domain.ModelRequest) (string, any, error) {
	switch p.config.Kind {
	case "anthropic":
		return anthropicRequest(r)
	case "gemini":
		return geminiRequest(r)
	case "openrouter", "openai-compatible":
		return chatRequest(r)
	default:
		return responsesRequest(r, p.config.Kind == "codex")
	}
}

func validBaseURL(u *url.URL) bool {
	return u.Host != "" && u.User == nil && (u.Scheme == "https" || u.Scheme == "http")
}
