package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

//nolint:gocognit,gocyclo // This full OAuth/JWKS and concurrent refresh scenario keeps its trust-boundary assertions together.
func TestDynamicRegistrationVerifiesIdentityAndSerializesRefresh(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer, nonce string
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{"issuer": issuer, "jwks_uri": issuer + "/jwks"})
		case "/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "fixture", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/api/accounts/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("client_id") != "issued" {
				t.Error("dynamic placeholder used for token exchange")
			}
			refresh := r.Form.Get("grant_type") == "refresh_token"
			expiry := 1
			if refresh {
				refreshes.Add(1)
				expiry = 3600
				if r.Form.Get("refresh_token") != "rotate-first" {
					t.Error("stale refresh token")
				}
			}
			writeJSON(t, w, map[string]any{"access_token": "access", "refresh_token": "rotate-next", "id_token": sign(t, key, map[string]any{"iss": issuer, "aud": "issued", "sub": "person", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce}), "token_type": "Bearer", "scope": SharingScope + " openid offline_access resource.invoke", "expires_in": expiry})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	issuer = server.URL
	service := New(Config{Home: t.TempDir(), Issuer: issuer, HTTPClient: server.Client()})
	attempt, err := service.Begin(t.Context(), LoginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(attempt.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	nonce = query.Get("nonce")
	if query.Get("client_id") != "dynamic_agent_client" || query.Get("code_challenge_method") != "S256" || query.Get("ext_agent_host_id") == "" {
		t.Fatalf("authorization %v", query)
	}
	credential, err := attempt.Complete(t.Context(), url.Values{"state": {query.Get("state")}, "code": {"fixture"}, "client_id": {"issued"}})
	if err != nil {
		t.Fatal(err)
	}
	credential.RefreshToken = "rotate-first"
	if err = service.Save(t.Context(), credential); err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			if _, e := New(service.config).TokenSource(credential.ID).Token(context.Background()); e != nil {
				t.Error(e)
			}
		})
	}
	group.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refresh calls %d", refreshes.Load())
	}
	entries, err := service.List(t.Context())
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries %v %v", entries, err)
	}
	info, err := os.Stat(service.credentialPath(credential.ID))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions %v %v", info, err)
	}
}

func TestCallbackRejectsStateAndConsentIsNotInferencePermission(t *testing.T) {
	service := New(Config{Home: t.TempDir()})
	attempt, err := service.Begin(t.Context(), LoginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = attempt.Complete(t.Context(), url.Values{"state": {"wrong"}, "code": {"x"}}); domain.CodeOf(err) != domain.ErrAuth {
		t.Fatalf("state error %v", err)
	}
	credential := Credentials{ID: "profile", Kind: "chatgpt", ClientID: "issued", Subject: "identity", AccessToken: "identity-only", ExpiresAt: time.Now().Add(time.Hour)}
	if err = service.Save(t.Context(), credential); err != nil {
		t.Fatal(err)
	}
	if _, err = service.TokenSource("profile").Token(t.Context()); domain.CodeOf(err) != domain.ErrAuth {
		t.Fatalf("scope error %v", err)
	}
	saved, err := service.Load(t.Context(), "profile")
	if err != nil || saved.AccessToken == "" {
		t.Fatalf("signed-in state lost: %v", err)
	}
}

func sign(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	value := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	hash := sha256.Sum256([]byte(value))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return value + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func TestCredentialsRejectUnsafeProfile(t *testing.T) {
	service := New(Config{Home: t.TempDir()})
	for _, id := range []string{"../outside", "/tmp/x", "", "a/b"} {
		if _, err := service.Load(t.Context(), id); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}

func TestLegacyBrowserUsesItsRegisteredLocalhostRedirect(t *testing.T) {
	service := New(Config{Home: t.TempDir()})
	attempt, err := service.Begin(t.Context(), LoginOptions{Kind: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(attempt.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("redirect_uri") != "http://localhost:1455/auth/callback" {
		t.Fatalf("legacy redirect %s", parsed.Query().Get("redirect_uri"))
	}
}

func TestAudienceArrayRejectsWrongAuthorizedParty(t *testing.T) {
	if validAudience(claims{Audience: json.RawMessage(`["issued"]`), AuthorizedParty: "other"}, "issued") {
		t.Fatal("accepted audience array with another authorized party")
	}
}
