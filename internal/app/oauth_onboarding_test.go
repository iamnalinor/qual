package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/auth"
)

type onboardingOAuthFixture struct {
	key          *rsa.PrivateKey
	issuer, kind string
	mu           sync.Mutex
	nonce        string
	approved     atomic.Bool
}

func TestInitOAuthUsesActualSavedRegistration(t *testing.T) {
	for _, kind := range []string{"chatgpt", "codex"} {
		t.Run(kind, func(t *testing.T) { testInitOAuthRegistration(t, kind) })
	}
}

func testInitOAuthRegistration(t *testing.T, kind string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &onboardingOAuthFixture{key: key, kind: kind}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
	defer server.Close()
	fixture.issuer = server.URL
	home := t.TempDir()
	service := auth.New(auth.Config{Home: home, Issuer: server.URL, HTTPClient: server.Client()})
	reader, writer := io.Pipe()
	defer closeOAuthPipes(t, reader, writer)

	var output bytes.Buffer
	a := &App{Home: home, JSON: true, Out: &output, Err: writer}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	browserDone := make(chan error, 1)
	go func() { browserDone <- followAuthorization(ctx, reader, server.Client()) }()
	options := newAccountOptions(kind, nil, kind == "codex")
	if options.Profile != "" {
		t.Fatalf("new OAuth tried to load an invented registration: %s", options.Profile)
	}
	id, err := a.connectAccount(ctx, service, options)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-browserDone; err != nil {
		t.Fatal(err)
	}
	verifyOAuthRegistration(ctx, t, a, service, server.URL, id, kind, output.String())

	if kind == "chatgpt" {
		testOAuthReauthorization(ctx, t, a, service, server, id)
	}
}

func testOAuthReauthorization(ctx context.Context, t *testing.T, a *App, service *auth.Service, server *httptest.Server, id string) {
	t.Helper()
	browserDone := make(chan error, 1)
	// Reauthorization intentionally reuses a real existing registration.
	nextReader, nextWriter := io.Pipe()
	defer closeOAuthPipes(t, nextReader, nextWriter)

	a.Err = nextWriter
	go func() { browserDone <- followAuthorization(ctx, nextReader, server.Client()) }()
	restored, e := a.connectAccount(ctx, service, LoginOptions{Kind: "chatgpt", Profile: id})
	if e != nil || restored != id {
		t.Fatalf("reauthorization changed registration: %q %v", restored, e)
	}
	if e = <-browserDone; e != nil {
		t.Fatal(e)
	}
}

func followAuthorization(ctx context.Context, input io.Reader, client *http.Client) error {
	scanner := bufio.NewScanner(input)
	if !scanner.Scan() {
		return scanner.Err()
	}
	var event struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, event.URL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	return response.Body.Close()
}

func (f *onboardingOAuthFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	switch r.URL.Path {
	case "/api/accounts/authorize", "/oauth/authorize":
		f.authorize(t, w, r)
	case "/codex/device":
		f.approved.Store(true)
		w.WriteHeader(http.StatusOK)
	case "/api/accounts/deviceauth/usercode":
		oauthJSON(t, w, map[string]string{"device_auth_id": "fixture-device", "user_code": "ABCD", "interval": "1"})
	case "/api/accounts/deviceauth/token":
		if !f.approved.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		oauthJSON(t, w, map[string]string{"authorization_code": "fixture-code", "code_verifier": "fixture-verifier"})
	case "/v1/models":
		oauthJSON(t, w, map[string]any{"data": []any{map[string]string{"id": "gpt-6-luna"}}, "models": []any{map[string]string{"slug": "gpt-6-luna", "display_name": "Fixture model", "visibility": "list"}}})
	case "/.well-known/openid-configuration":
		oauthJSON(t, w, map[string]string{"issuer": f.issuer, "jwks_uri": f.issuer + "/jwks"})
	case "/jwks":
		oauthJSON(t, w, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "fixture", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())}}})
	case "/api/accounts/oauth/token", "/oauth/token":
		f.token(t, w, r)
	default:
		t.Errorf("unexpected OAuth path %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *onboardingOAuthFixture) authorize(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	query := r.URL.Query()
	f.mu.Lock()
	f.nonce = query.Get("nonce")
	f.mu.Unlock()
	callback, err := url.Parse(query.Get("redirect_uri"))
	if err != nil {
		t.Error(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	client := query.Get("client_id")
	if client == "dynamic_agent_client" {
		client = "fixture-client"
	}
	callback.RawQuery = url.Values{"state": {query.Get("state")}, "code": {"fixture-code"}, "client_id": {client}}.Encode()
	// #nosec G710 -- This local OAuth fixture follows only the test browser login loopback callback.
	http.Redirect(w, r, callback.String(), http.StatusFound)
}

func (f *onboardingOAuthFixture) token(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Error(err)
	}
	f.mu.Lock()
	nonce := f.nonce
	f.mu.Unlock()
	claims := map[string]any{"iss": f.issuer, "aud": r.Form.Get("client_id"), "sub": "fixture-person", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "fixture-account"}}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": "fixture"})
	if err != nil {
		t.Error(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Error(err)
	}
	content := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(content))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Error(err)
	}
	scope := "openid offline_access"
	if f.kind == "chatgpt" {
		scope += " " + auth.SharingScope
	}
	oauthJSON(t, w, map[string]any{"access_token": "fixture-access-token", "refresh_token": "fixture-refresh-token", "id_token": content + "." + base64.RawURLEncoding.EncodeToString(signature), "expires_in": 3600, "scope": scope, "token_type": "Bearer"})
}

func oauthJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func verifyOAuthRegistration(ctx context.Context, t *testing.T, a *App, service *auth.Service, issuer, id, kind, output string) {
	t.Helper()
	record, err := service.Load(ctx, id)
	if err != nil || record.Kind != kind || id == "" || id == kind {
		t.Fatalf("actual saved registration %q: %v", id, err)
	}
	if record.AccessToken != "fixture-access-token" {
		t.Fatal("OAuth token exchange was not completed")
	}
	if strings.Contains(output, record.AccessToken) {
		t.Fatal("secret exposed in init result")
	}
	var diagnostics bytes.Buffer
	a.Err = &diagnostics
	settings, accepted, e := a.initialize(ctx, InitOptions{Connection: "account", Kind: kind, Endpoint: issuer + "/v1", Credential: "profile:" + id, Model: "gpt-6-luna", Accept: true})
	if e != nil || !accepted || settings.Connections["account"].Credential != "profile:"+record.ID {
		t.Fatalf("OAuth init did not activate actual saved account: %v", e)
	}
}

func closeOAuthPipes(t *testing.T, closers ...io.Closer) {
	t.Helper()
	for _, closer := range closers {
		if err := closer.Close(); err != nil {
			t.Log(err)
		}
	}
}
