package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type oauthFixture struct {
	key                 *rsa.PrivateKey
	issuer, nonce, kind string
	tokens              atomic.Int32
}

func newOAuthFixture(t *testing.T, kind string) (*oauthFixture, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &oauthFixture{key: key, kind: kind}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
	fixture.issuer = server.URL
	t.Cleanup(server.Close)
	return fixture, server
}

func (f *oauthFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeJSON(t, w, map[string]string{"issuer": f.issuer, "jwks_uri": f.issuer + "/jwks"})
	case "/jwks":
		writeJSON(t, w, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "fixture", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())}}})
	case "/api/accounts/oauth/token", "/oauth/token":
		f.issue(t, w, r)
	case "/api/accounts/deviceauth/usercode":
		writeJSON(t, w, map[string]any{"device_auth_id": "device-fixture", "user_code": "ABCD", "interval": "1"})
	case "/api/accounts/deviceauth/token":
		writeJSON(t, w, map[string]string{"authorization_code": "device-code", "code_verifier": "device-verifier"})
	default:
		t.Errorf("unexpected auth endpoint %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *oauthFixture) issue(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	f.tokens.Add(1)
	if err := r.ParseForm(); err != nil {
		t.Error(err)
	}
	clientID := "issued"
	path := "/api/accounts/oauth/token"
	scope := "openid offline_access " + SharingScope
	if f.kind == "codex" {
		clientID = legacyClientID
		path = "/oauth/token"
		scope = "openid offline_access"
	}
	if r.URL.Path != path || r.Form.Get("client_id") != clientID {
		t.Errorf("wrong billing/auth route %s", r.URL.Path)
	}
	claims := map[string]any{"iss": f.issuer, "aud": clientID, "sub": "fixture-person", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": f.nonce, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "fixture-account"}}
	writeJSON(t, w, map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "id_token": sign(t, f.key, claims), "token_type": "Bearer", "expires_in": 3600, "scope": scope})
}

//nolint:gocognit,gocyclo // Both complete browser routes exercise URL generation, real loopback callback, exchange and persistence.
func TestBrowserLoginLoopbackAndLegacyRoute(t *testing.T) {
	for _, kind := range []string{"chatgpt", "codex"} {
		t.Run(kind, func(t *testing.T) {
			fixture, server := newOAuthFixture(t, kind)
			service := New(Config{Home: t.TempDir(), Issuer: server.URL, HTTPClient: server.Client()})
			credential, err := service.BrowserLogin(t.Context(), LoginOptions{Kind: kind}, func(endpoint string) error {
				parsed, parseErr := url.Parse(endpoint)
				if parseErr != nil {
					return parseErr
				}
				query := parsed.Query()
				fixture.nonce = query.Get("nonce")
				expected := "/api/accounts/authorize"
				if kind == "codex" {
					expected = "/oauth/authorize"
				}
				if parsed.Path != expected {
					t.Errorf("authorize path %s", parsed.Path)
				}
				callback, parseErr := url.Parse(query.Get("redirect_uri"))
				if parseErr != nil {
					return parseErr
				}
				callback.RawQuery = url.Values{"state": {query.Get("state")}, "code": {"browser-code"}, "client_id": {query.Get("client_id")}}.Encode()
				if kind == "chatgpt" {
					values := callback.Query()
					values.Set("client_id", "issued")
					callback.RawQuery = values.Encode()
				}
				request, requestErr := http.NewRequestWithContext(t.Context(), http.MethodGet, callback.String(), nil)
				if requestErr != nil {
					return requestErr
				}
				response, requestErr := server.Client().Do(request)
				if requestErr != nil {
					return requestErr
				}
				return response.Body.Close()
			})
			if err != nil {
				t.Fatal(err)
			}
			if credential.Kind != kind || credential.AccountID != "fixture-account" || fixture.tokens.Load() != 1 {
				t.Fatalf("credentials %#v", Profile{ID: credential.ID, Kind: credential.Kind})
			}
		})
	}
}

func TestLegacyDeviceUsesSeparateTokenEndpoint(t *testing.T) {
	fixture, server := newOAuthFixture(t, "codex")
	service := New(Config{Home: t.TempDir(), Issuer: server.URL, HTTPClient: server.Client()})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	credential, err := service.DeviceLogin(ctx, func(code DeviceCode) error {
		if code.UserCode != "ABCD" || !strings.HasSuffix(code.VerificationURL, "/codex/device") {
			t.Errorf("device display %v", code)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Kind != "codex" || fixture.tokens.Load() != 1 {
		t.Fatalf("legacy device route %s", credential.Kind)
	}
}

func TestIdentityRejectsWrongNonceAudienceAndSignature(t *testing.T) {
	fixture, server := newOAuthFixture(t, "chatgpt")
	service := New(Config{Home: t.TempDir(), Issuer: server.URL, HTTPClient: server.Client()})
	for _, test := range []struct {
		name            string
		audience, nonce string
		expiry          int64
	}{{"nonce", "issued", "wrong", time.Now().Add(time.Hour).Unix()}, {"audience", "other", "nonce", time.Now().Add(time.Hour).Unix()}, {"expired", "issued", "nonce", time.Now().Add(-time.Hour).Unix()}} {
		t.Run(test.name, func(t *testing.T) {
			token := sign(t, fixture.key, map[string]any{"iss": fixture.issuer, "aud": test.audience, "sub": "person", "iat": time.Now().Unix(), "exp": test.expiry, "nonce": test.nonce})
			if _, err := service.verifyIdentity(t.Context(), token, "issued", "nonce"); err == nil {
				t.Error("accepted invalid identity")
			}
		})
	}
	token := sign(t, fixture.key, map[string]any{"iss": fixture.issuer, "aud": "issued", "sub": "person", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": "nonce"})
	parts := strings.Split(token, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"attacker"}`))
	if _, err := service.verifyIdentity(t.Context(), strings.Join(parts, "."), "issued", "nonce"); err == nil {
		t.Error("accepted forged signature")
	}
}

func TestStorageRejectsSymlinksAndReadableCredentials(t *testing.T) {
	service := New(Config{Home: t.TempDir()})
	credential := Credentials{ID: "fixture", Kind: "chatgpt"}
	if err := service.Save(t.Context(), credential); err != nil {
		t.Fatal(err)
	}
	path := service.credentialPath("fixture")
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: deliberately unsafe fixture verifies readable credentials are rejected.
		t.Fatal(err)
	}
	if _, err := service.Load(t.Context(), "fixture"); err == nil {
		t.Error("accepted readable credentials")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(`{"schema_version":1,"id":"fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Load(t.Context(), "fixture"); err == nil {
		t.Error("accepted symlink")
	}
}

func TestConcurrentFirstLoginKeepsOneStableHostID(t *testing.T) {
	home := t.TempDir()
	ids := make(chan string, 16)
	errors := make(chan error, 16)
	for range 16 {
		go func() {
			service := New(Config{Home: home})
			attempt, err := service.Begin(t.Context(), LoginOptions{})
			if err != nil {
				errors <- err
				return
			}
			parsed, err := url.Parse(attempt.URL)
			if err != nil {
				errors <- err
				return
			}
			ids <- parsed.Query().Get("ext_agent_host_id")
		}()
	}
	var first string
	for range 16 {
		select {
		case id := <-ids:
			if first == "" {
				first = id
			}
			if id != first {
				t.Error("host identifier changed between concurrent sign-ins")
			}
		case err := <-errors:
			t.Fatal(err)
		}
	}
}

func TestRefreshFailurePreservesUnknownOutcomeWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	service := New(Config{Home: t.TempDir(), Issuer: server.URL, HTTPClient: server.Client()})
	credential := Credentials{ID: "profile", Kind: "chatgpt", ClientID: "issued", Subject: "person", AccessToken: "old", RefreshToken: "rotating", Scopes: []string{SharingScope}, ExpiresAt: time.Now().Add(-time.Hour)}
	if err := service.Save(t.Context(), credential); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := service.TokenSource("profile").Token(t.Context()); err == nil {
			t.Error("refresh unexpectedly succeeded")
		}
	}
	if attempts.Load() != 1 {
		t.Fatalf("replayed rotating token %d times", attempts.Load())
	}
	saved, err := service.Load(t.Context(), "profile")
	if err != nil {
		t.Fatal(err)
	}
	if !saved.RefreshBlocked || saved.AccessToken != "old" {
		t.Error("lost refresh intent or prior state")
	}
}
