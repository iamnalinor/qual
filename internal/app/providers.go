package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/port"
	"github.com/iamnalinor/qual/internal/provider"
)

type lazyProvider struct {
	app          *App
	connection   config.Connection
	models       []domain.ModelInfo
	name, origin string
	modelOrigins map[string]string
}

func (p lazyProvider) adapter(ctx context.Context) (port.Provider, error) {
	c := p.connection
	kind := providerKind(c.Kind)
	selected := provider.Config{Kind: kind, BaseURL: c.Endpoint, Capabilities: p.models}
	if kind == "chatgpt" || kind == "codex" {
		if !strings.HasPrefix(c.Credential, "profile:") {
			return nil, &domain.Error{Code: domain.ErrAuth, Message: "subscription connection requires an explicit profile reference"}
		}
		service, err := p.app.authService()
		if err != nil {
			return nil, err
		}
		id := strings.TrimPrefix(c.Credential, "profile:")
		record, err := service.Load(ctx, id)
		if err != nil {
			return nil, &domain.Error{Code: domain.ErrAuth, Message: "The saved sign-in is unavailable. Run qual auth list to inspect saved sign-ins, then qual init to connect the account again.", Cause: err}
		}
		if record.Kind != kind {
			return nil, &domain.Error{Code: domain.ErrAuth, Message: "credential billing route differs from selected provider"}
		}
		selected.TokenSource = service.TokenSource(id)
	} else {
		key, err := p.app.apiKey(ctx, c.Credential, kind)
		if err != nil {
			return nil, err
		}
		selected.APIKey = key
	}
	return provider.New(selected)
}

func (p lazyProvider) Run(ctx context.Context, request domain.ModelRequest, emit func(domain.ModelEvent) error) error {
	adapter, err := p.adapter(ctx)
	if err != nil {
		return p.requestError(request.Model, err, false)
	}
	err = adapter.Run(ctx, request, emit)
	if err != nil {
		return p.requestError(request.Model, err, true)
	}
	return nil
}

func (p lazyProvider) Models(ctx context.Context) ([]domain.ModelInfo, error) {
	adapter, err := p.adapter(ctx)
	if err != nil {
		return nil, err
	}
	return adapter.Models(ctx)
}

func (a *App) secret(ctx context.Context, ref, kind string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if strings.HasPrefix(ref, "env:") {
		value := os.Getenv(strings.TrimPrefix(ref, "env:"))
		if value == "" {
			return "", &domain.Error{Code: domain.ErrAuth, Message: "The selected credential environment variable is empty. Set that variable, or run qual init to connect an account."}
		}
		return value, nil
	}
	if !strings.HasPrefix(ref, "profile:") {
		return "", invalid("credential must be an environment or profile reference")
	}
	service, err := a.authService()
	if err != nil {
		return "", err
	}
	record, err := service.Load(ctx, strings.TrimPrefix(ref, "profile:"))
	if err != nil {
		return "", &domain.Error{Code: domain.ErrAuth, Message: "The saved sign-in is unavailable. Run qual auth list to inspect saved sign-ins, then qual init to connect the account again.", Cause: err}
	}
	if kind != "" && record.Kind != kind {
		return "", &domain.Error{Code: domain.ErrAuth, Message: "credential belongs to a different provider route"}
	}
	return record.AccessToken, nil
}

func (a *App) providers(effective config.Effective) map[string]port.Provider {
	result := map[string]port.Provider{}
	for name, connection := range effective.Settings.Connections {
		result[name] = lazyProvider{app: a, connection: connection, models: capabilities(effective, name), name: name, origin: originLabel(effective.Provenance["connections."+name+".credential"]), modelOrigins: providerModelOrigins(effective, name)}
	}
	return result
}

func capabilities(e config.Effective, name string) []domain.ModelInfo {
	c := e.Settings.Connections[name]
	if len(c.Capabilities) == 0 {
		return nil
	}
	var result []domain.ModelInfo
	agents := append([]config.Agent{e.Settings.Leader, e.Settings.Setup}, e.Settings.Reviewers...)
	for _, agent := range agents {
		if agent.Provider == name {
			m := domain.ModelInfo{ID: agent.Model, Provider: c.Kind}
			for _, v := range c.Capabilities {
				switch v {
				case "tools":
					m.Tools = true
				case "streaming":
					m.Streaming = true
				case "reasoning":
					m.ReasoningLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
				}
			}
			result = append(result, m)
		}
	}
	return result
}

// Models lists the selected connection's catalog; account routes use account discovery.
func (a *App) Models(ctx context.Context, name string) error {
	e, err := a.Config(ctx, "")
	if err != nil {
		return err
	}
	if len(e.Settings.Connections) == 0 {
		return invalid("No provider is configured. Run qual init to connect an account or API key and choose your models.")
	}
	if name == "" && len(e.Settings.Connections) > 1 {
		names := connectionNames(e)
		return invalid("Several accounts are configured: " + strings.Join(names, ", ") + ". List models with qual models list --connection " + names[0] + ". To configure accounts and models, run qual init.")
	}
	p, err := a.selected(e, name)
	if err != nil {
		return err
	}
	models, err := p.Models(ctx)
	if err != nil {
		return err
	}
	if err = a.selectUI(e); err != nil {
		return err
	}
	return a.renderModels(models, p.connection.Kind == "chatgpt")
}

func (a *App) selected(e config.Effective, name string) (lazyProvider, error) {
	if name == "" {
		names := connectionNames(e)
		if len(names) == 1 {
			name = names[0]
		}
	}
	c, ok := e.Settings.Connections[name]
	if !ok {
		return lazyProvider{}, invalid("This provider account is not configured. Run qual init to connect it, or qual config show to inspect your configured accounts.")
	}
	return lazyProvider{app: a, connection: c, models: capabilities(e, name)}, nil
}

func (a *App) checkConnection(ctx context.Context, c config.Connection, selected ...string) (retErr error) {
	c.Kind = providerKind(c.Kind)
	p := lazyProvider{app: a, connection: c}
	adapter, err := p.adapter(ctx)
	if err != nil {
		return err
	}
	if c.Kind == "chatgpt" {
		models, loadErr := adapter.Models(ctx)
		if loadErr != nil {
			return loadErr
		}
		return requireModels(models, selected)
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = map[string]string{"openai": "https://api.openai.com/v1", "anthropic": "https://api.anthropic.com/v1", "gemini": "https://generativelanguage.googleapis.com/v1beta", "openrouter": "https://openrouter.ai/api/v1", "codex": "https://chatgpt.com/backend-api/codex"}[c.Kind]
	}
	key, err := a.secret(ctx, c.Credential, c.Kind)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/models", nil)
	if err != nil {
		return invalid("invalid connection endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	probeHeaders(req, c.Kind, key)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return &domain.Error{Code: domain.ErrAuth, Message: "Could not check provider authentication. Check the endpoint and network, then run qual init again.", Cause: err}
	}
	defer func() {
		retErr = errors.Join(retErr, response.Body.Close())
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &domain.Error{Code: domain.ErrAuth, Message: fmt.Sprintf("Provider rejected the authentication check (HTTP %d). Check the account credentials and endpoint, then run qual init again.", response.StatusCode)}
	}
	if len(selected) == 0 {
		return nil
	}
	return checkModels(response.Body, selected)
}

func checkModels(body io.Reader, selected []string) error {
	var data struct {
		Data   []struct{ ID string }
		Models []struct{ ID, Name, Slug string }
	}
	if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&data); err != nil {
		return &domain.Error{Code: domain.ErrIncomplete, Message: "Provider model discovery returned an invalid catalog; check the configured endpoint with qual init.", Cause: err}
	}
	models := make([]domain.ModelInfo, 0, len(data.Data)+len(data.Models))
	for _, m := range data.Data {
		models = append(models, domain.ModelInfo{ID: m.ID})
	}
	for _, m := range data.Models {
		id := m.ID
		if id == "" {
			id = m.Slug
		}
		if id == "" {
			id = strings.TrimPrefix(m.Name, "models/")
		}
		models = append(models, domain.ModelInfo{ID: id})
	}
	return requireModels(models, selected)
}

func requireModels(models []domain.ModelInfo, selected []string) error {
	available := map[string]bool{}
	for _, model := range models {
		available[model.ID] = true
	}
	for _, id := range selected {
		if !available[id] {
			return &domain.Error{Code: domain.ErrUnsupportedCapability, Message: "Model " + id + " is not available for this account. Credentials were supplied, but model access was not found. Check access in your provider account, then choose an available model with qual init --model " + commandArgument(id) + "."}
		}
	}
	return nil
}

func probeHeaders(req *http.Request, kind, key string) {
	switch kind {
	case "anthropic":
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		req.Header.Del("Authorization")
		req.Header.Set("x-goog-api-key", key)
	}
}

func providerKind(kind string) string {
	if kind == "compatible" {
		return "openai-compatible"
	}
	return kind
}

func (a *App) apiKey(ctx context.Context, ref, kind string) (string, error) {
	key, err := a.secret(ctx, ref, kind)
	if err != nil {
		return "", err
	}
	if key == "" && kind != "openai-compatible" {
		return "", &domain.Error{Code: domain.ErrAuth, Message: "No API key is selected for this provider. Run qual init to connect an account or key and choose models."}
	}
	return key, nil
}

func connectionNames(e config.Effective) []string {
	names := make([]string, 0, len(e.Settings.Connections))
	for name := range e.Settings.Connections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (p lazyProvider) requestError(model string, cause error, credentialsSupplied bool) error {
	if p.name == "" {
		return cause
	}
	action := "Configure credentials with qual init --connection " + commandArgument(p.name) + " --kind " + commandArgument(p.connection.Kind) + " --model " + commandArgument(model) + " --add-account."
	if credentialsSupplied {
		action = "Credentials were supplied. Check provider availability, token validity and model access; use qual init to update this account or choose an available model."
	}
	if domain.CodeOf(cause) == domain.ErrUnsupportedCapability && strings.Contains(cause.Error(), "capabilities unknown") {
		action = "This custom model needs explicit verified tool capabilities. If its provider documents tool calling and streaming, configure them with qual init --connection " + commandArgument(p.name) + " --kind " + commandArgument(p.connection.Kind) + " --model " + commandArgument(model) + " --capability tools --capability streaming --add-account."
	}
	origin := p.origin
	if selected := p.modelOrigins[model]; selected != "" {
		origin = selected + "; credentials: " + origin
	}
	message := fmt.Sprintf("Request for model %s via provider %s (account %s, selected by %s) failed. %s", model, p.connection.Kind, p.name, origin, action)
	return &domain.Error{Code: domain.CodeOf(cause), Message: message, Cause: cause}
}

func providerModelOrigins(e config.Effective, name string) map[string]string {
	origins := map[string]string{}
	for _, agent := range e.Agents {
		if agent.Provider == name {
			origins[agent.Model] = originLabel(modelOrigin(e, agent))
		}
	}
	return origins
}
