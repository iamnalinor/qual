package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestHTTPFailureIncludesStructuredProviderReason(t *testing.T) {
	cases := []struct {
		name, kind, body, want string
		status                 int
	}{
		{"parameter", "openai", `{"error":{"message":"Unsupported parameter: temperature","code":"unsupported_parameter","type":"invalid_request_error"}}`, "Unsupported parameter: temperature", 400},
		{"model access", "anthropic", `{"type":"error","error":{"type":"not_found_error","message":"Model fixture-model is not available to your account"}}`, "Model fixture-model is not available", 404},
		{"permission", "gemini", `{"error":{"code":403,"message":"API key cannot access this model","status":"PERMISSION_DENIED"}}`, "PERMISSION_DENIED", 403},
		{"quota", "openai", `{"error":{"message":"Current quota exhausted","type":"insufficient_quota","code":"insufficient_quota"}}`, "insufficient_quota", 429},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tc.status)
				if _, err := fmt.Fprint(w, tc.body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			p, err := New(Config{Kind: tc.kind, BaseURL: server.URL, APIKey: "fixture-key"})
			if err != nil {
				t.Fatal(err)
			}
			err = p.Run(t.Context(), domain.ModelRequest{Model: "fixture"}, func(domain.ModelEvent) error { return nil })
			var coded *domain.Error
			if !errors.As(err, &coded) || !strings.Contains(err.Error(), tc.want) || coded.HTTPStatus != tc.status || coded.RetryAfter != 2*time.Second || coded.Retryable != (tc.status == 429) {
				t.Fatalf("HTTP diagnostic: %v", err)
			}
		})
	}
}

func TestHTTPFailureDoesNotExposeUnrecognizedResponse(t *testing.T) {
	for _, body := range []string{
		`<html>private prompt body</html>`,
		`{"message":"private prompt body"}`,
		`{"error":{"message":"private prompt body"`,
		`{"error":{"message":"` + strings.Repeat("private-prompt ", 2000) + `"}}`,
	} {
		t.Run(fmt.Sprintf("bytes-%d", len(body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(400)
				if _, err := fmt.Fprint(w, body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			p, err := New(Config{Kind: "openai", BaseURL: server.URL, APIKey: "fixture-key"})
			if err != nil {
				t.Fatal(err)
			}
			err = p.Run(t.Context(), domain.ModelRequest{Model: "fixture"}, func(domain.ModelEvent) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "Bad Request") || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe fallback: %v", err)
			}
		})
	}
}

func TestCatalogHTTPReasonRedactsCurrentCredentials(t *testing.T) {
	const token = "fixture-current-oauth-token"
	const account = "fixture-current-account"
	message := "model access denied for " + token + " account " + account + " https://name:password@proxy.test/token-path?key=query-secret\n\x1b[31m"
	body, err := json.Marshal(map[string]any{"error": map[string]any{"message": message, "code": "model_access_denied"}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	p, err := New(Config{Kind: "chatgpt", BaseURL: server.URL, TokenSource: TokenSourceFunc(func(context.Context) (Credential, error) {
		return Credential{AccessToken: token, AccountID: account}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Models(t.Context())
	if err == nil || !strings.Contains(err.Error(), "model access denied") || !strings.Contains(err.Error(), "model_access_denied") || domain.CodeOf(err) != domain.ErrAuth {
		t.Fatalf("catalog HTTP reason: %v", err)
	}
	for _, secret := range []string{token, account, "password", "token-path", "query-secret", "\n", "\x1b"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe HTTP reason: %q", err.Error())
		}
	}
}

func TestHTTPFailureCapsReasonAfterRedaction(t *testing.T) {
	const key = "fixture-long-credential-value"
	message := strings.Repeat("x", 1000) + key + strings.Repeat("y", 3000)
	body, err := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		if _, err := w.Write(body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	p, err := New(Config{Kind: "openai", BaseURL: server.URL, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Run(t.Context(), domain.ModelRequest{Model: "fixture"}, func(domain.ModelEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "xxxx") || len(err.Error()) > 1200 || strings.Contains(err.Error(), "fixture-long") {
		t.Fatalf("uncapped or unsafe HTTP reason (%d bytes): %v", len(fmt.Sprint(err)), err)
	}
}
