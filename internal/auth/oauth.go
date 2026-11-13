package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"strings"
	"sync"
	"time"
)

// LoginOptions selects a saved registration or starts a new one. Kind is chatgpt
// by default; codex explicitly selects the separate legacy authorization route.
type (
	LoginOptions struct{ Kind, Profile, RedirectURI string }
	// Attempt is an expiring single-use PKCE authorization transaction.
	Attempt struct {
		URL                                      string
		service                                  *Service
		options                                  LoginOptions
		previous                                 Credentials
		state, nonce, verifier, hostID, clientID string
		expires                                  time.Time
		mu                                       sync.Mutex
		used                                     bool
	}
)

// Begin creates a browser authorization transaction without opening a browser.
// BrowserLogin starts the loopback listener before calling this method.
func (s *Service) Begin(ctx context.Context, options LoginOptions) (*Attempt, error) {
	if options.Kind == "" {
		options.Kind = "chatgpt"
	}
	if options.Kind != "chatgpt" && options.Kind != "codex" {
		return nil, authError("unknown OAuth route")
	}
	if options.RedirectURI == "" {
		options.RedirectURI = "http://127.0.0.1:1455/auth/callback"
		if options.Kind == "codex" {
			options.RedirectURI = "http://localhost:1455/auth/callback"
		}
	}
	if err := validateRedirect(options.RedirectURI, options.Kind); err != nil {
		return nil, err
	}
	attempt := &Attempt{service: s, options: options, clientID: "dynamic_agent_client", expires: time.Now().Add(5 * time.Minute)}
	if err := attempt.prepare(ctx); err != nil {
		return nil, err
	}
	attempt.URL = attempt.authorizeURL()
	return attempt, nil
}

func validateRedirect(value, kind string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || !validCallbackHost(u.Hostname(), kind) || u.Port() == "" || u.Path != "/auth/callback" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return authError("OAuth callback must be an HTTP 127.0.0.1 loopback at /auth/callback")
	}
	return nil
}

func (a *Attempt) prepare(ctx context.Context) error {
	var err error
	if a.options.Profile != "" {
		a.previous, err = a.service.Load(ctx, a.options.Profile)
		if err != nil {
			return err
		}
		if a.previous.Kind != a.options.Kind {
			return authError("selected profile belongs to a different OAuth route")
		}
		a.clientID = a.previous.ClientID
	}
	if a.options.Kind == "codex" {
		a.clientID = legacyClientID
	}
	a.hostID, err = a.service.hostID(ctx)
	if err != nil {
		return err
	}
	a.state, err = randomValue()
	if err != nil {
		return err
	}
	a.nonce, err = randomValue()
	if err != nil {
		return err
	}
	a.verifier, err = randomValue()
	return err
}

func (a *Attempt) authorizeURL() string {
	challenge := sha256.Sum256([]byte(a.verifier))
	query := url.Values{"client_id": {a.clientID}, "response_type": {"code"}, "redirect_uri": {a.options.RedirectURI}, "scope": {"openid profile email offline_access resource.invoke " + SharingScope}, "resource": {resource}, "state": {a.state}, "nonce": {a.nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "ext_agent_host_id": {a.hostID}}
	path := "/api/accounts/authorize"
	if a.clientID == "dynamic_agent_client" {
		query.Set("agent_name_hint", "Qual")
	}
	if a.previous.IDToken != "" {
		query.Set("id_token_hint", a.previous.IDToken)
	}
	if a.previous.Email != "" {
		query.Set("login_hint", a.previous.Email)
	}
	if a.options.Kind == "codex" {
		path = "/oauth/authorize"
		query.Set("scope", "openid profile email offline_access")
		query.Del("resource")
		query.Del("ext_agent_host_id")
		query.Set("codex_cli_simplified_flow", "true")
		query.Set("id_token_add_organizations", "true")
		query.Set("originator", "qual")
	}
	return a.service.config.Issuer + path + "?" + query.Encode()
}

// Complete validates callback state before consuming any authorization result.
func (a *Attempt) Complete(ctx context.Context, query url.Values) (Credentials, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used || time.Now().After(a.expires) {
		return Credentials{}, authError("authorization attempt expired or was already consumed")
	}
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(a.state)) != 1 {
		return Credentials{}, authError("OAuth state mismatch")
	}
	a.used = true
	if query.Get("error") != "" {
		return Credentials{}, authError("authorization was declined; existing account credentials were preserved")
	}
	if query.Get("code") == "" {
		return Credentials{}, authError("authorization code missing")
	}
	clientID, err := a.callbackClient(query.Get("client_id"))
	if err != nil {
		return Credentials{}, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {query.Get("code")}, "code_verifier": {a.verifier}, "redirect_uri": {a.options.RedirectURI}}
	if a.options.Kind == "chatgpt" {
		form.Set("resource", resource)
	}
	tokens, err := a.service.tokenRequest(ctx, a.options.Kind, form)
	if err != nil {
		return Credentials{}, err
	}
	credential, err := a.service.registration(ctx, tokens, a.options.Kind, clientID, a.nonce, a.hostID, a.previous)
	if err != nil {
		return Credentials{}, err
	}
	if err = a.service.Save(ctx, credential); err != nil {
		return Credentials{}, err
	}
	return credential, nil
}

func (a *Attempt) callbackClient(issued string) (string, error) {
	if a.clientID == "dynamic_agent_client" {
		if issued == "" || issued == "dynamic_agent_client" {
			return "", authError("registration did not return an issued client ID")
		}
		return issued, nil
	}
	if issued != "" && issued != a.clientID {
		return "", authError("callback changed the selected account client ID")
	}
	return a.clientID, nil
}

func (s *Service) registration(ctx context.Context, t tokenResponse, kind, clientID, nonce, hostID string, previous Credentials) (Credentials, error) {
	claims, err := s.verifyIdentity(ctx, t.IDToken, clientID, nonce)
	if err != nil {
		return Credentials{}, err
	}
	if previous.Subject != "" && previous.Subject != claims.Subject {
		return Credentials{}, authError("authorization returned a different account")
	}
	c := Credentials{SchemaVersion: 1, ID: profileID(kind, clientID, claims.Subject), Kind: kind, Issuer: s.config.Issuer, Subject: claims.Subject, Email: claims.Email, ClientID: clientID, HostID: hostID, AccountID: claims.AccountID, IDToken: t.IDToken, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, Scopes: strings.Fields(t.Scope), ExpiresAt: time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)}
	if previous.ID != "" {
		c.ID = previous.ID
	}
	return c, nil
}

func validCallbackHost(host, kind string) bool {
	if kind == "codex" {
		return host == "localhost"
	}
	return host == "127.0.0.1"
}
