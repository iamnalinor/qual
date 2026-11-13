// Package auth implements explicit ChatGPT token-sharing and legacy Codex login.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/provider"
)

// SharingScope is required for ChatGPT plan inference, separately from identity.
const (
	SharingScope   = "chatgpt.tokens.use.direct"
	legacyClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	resource       = "https://api.openai.com/v1"
)

// Keyring is an optional owner-controlled credential backend. No fallback occurs
// when a configured keyring returns an error.
type (
	Keyring interface {
		Get(context.Context, string) ([]byte, error)
		Set(context.Context, string, []byte) error
		Delete(context.Context, string) error
		List(context.Context) ([]string, error)
	}
	// Config selects protected credential storage and OAuth issuer. Issuer override is
	// intended for explicitly configured trusted deployments and offline fixtures.
	Config struct {
		Home, Issuer string
		HTTPClient   *http.Client
		Keyring      Keyring
	}
	// Service manages protected account registrations and serialized refresh.
	Service struct {
		config    Config
		client    *http.Client
		storageMu sync.Mutex
	}
	// Credentials is persisted only to owner-only storage, never command output.
	Credentials struct {
		SchemaVersion  int       `json:"schema_version"`
		ID             string    `json:"id"`
		Kind           string    `json:"kind"`
		Issuer         string    `json:"issuer"`
		Subject        string    `json:"subject"`
		Email          string    `json:"email,omitempty"`
		ClientID       string    `json:"client_id"`
		HostID         string    `json:"ext_agent_host_id"`
		AccountID      string    `json:"account_id,omitempty"`
		IDToken        string    `json:"id_token"`
		AccessToken    string    `json:"access_token"`
		RefreshToken   string    `json:"refresh_token"`
		Scopes         []string  `json:"scopes"`
		ExpiresAt      time.Time `json:"expires_at"`
		RefreshBlocked bool      `json:"refresh_blocked,omitempty"`
	}
)

// Profile is the safe account metadata returned to list commands.
type Profile struct {
	ID, Kind, Email, Subject, ClientID string
	SharingEnabled                     bool
	ExpiresAt                          time.Time
}

// New builds an authentication service without any network requests.
func New(c Config) *Service {
	if c.Issuer == "" {
		c.Issuer = "https://auth.openai.com"
	}
	c.Issuer = strings.TrimRight(c.Issuer, "/")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copyClient := *client
	if copyClient.Timeout <= 0 {
		copyClient.Timeout = 30 * time.Second
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Service{config: c, client: &copyClient}
}

func authError(message string) *domain.Error {
	return &domain.Error{Code: domain.ErrAuth, Message: message}
}

func randomValue() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", authError("secure randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

type tokenSource struct {
	service *Service
	id      string
}

// TokenSource binds one selected profile; it never looks for API-key credentials.
func (s *Service) TokenSource(id string) provider.TokenSource { return tokenSource{service: s, id: id} }

func (source tokenSource) Token(ctx context.Context) (result provider.Credential, resultErr error) {
	s := source.service
	lock, err := s.lockProfile(ctx, source.id)
	if err != nil {
		return provider.Credential{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	if err := ctx.Err(); err != nil {
		return provider.Credential{}, err
	}
	credentials, err := s.Load(ctx, source.id)
	if err != nil {
		return provider.Credential{}, err
	}
	if credentials.Kind == "chatgpt" && !hasScope(credentials.Scopes, SharingScope) {
		return provider.Credential{}, authError("signed in; ChatGPT plan sharing is disabled, authorize plan usage before inference")
	}
	if credentials.RefreshBlocked {
		return provider.Credential{}, authError("previous refresh outcome is unknown; sign in again before inference")
	}
	if time.Until(credentials.ExpiresAt) < time.Minute {
		credentials, err = s.refresh(ctx, credentials)
		if err != nil {
			return provider.Credential{}, err
		}
	}
	return provider.Credential{AccessToken: credentials.AccessToken, AccountID: credentials.AccountID, Kind: credentials.Kind}, nil
}

func hasScope(scopes []string, scope string) bool {
	for _, value := range scopes {
		if value == scope {
			return true
		}
	}
	return false
}
