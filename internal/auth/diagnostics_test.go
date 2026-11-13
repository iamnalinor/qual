package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeviceDiagnosticsRetainSafeCauses(t *testing.T) {
	const secret = "fixture-device-secret"
	cause := fmt.Errorf("proxy unavailable for %s https://name:pass@proxy.test/token?secret=hidden\nend", secret)
	service := New(Config{HTTPClient: &http.Client{Transport: diagnosticTransport(func(*http.Request) (*http.Response, error) { return nil, cause })}})
	_, _, err := service.deviceRequest(context.Background(), "token", map[string]string{"device_code": secret})
	if err == nil || !strings.Contains(err.Error(), "proxy unavailable") || !errors.Is(err, cause) || domain.CodeOf(err) != domain.ErrAuth {
		t.Fatalf("device transport: %v", err)
	}
	for _, text := range []string{secret, "pass@", "/token?", "hidden", "\n"} {
		if strings.Contains(err.Error(), text) {
			t.Fatalf("unsafe error: %q", err.Error())
		}
	}
	service = New(Config{Issuer: "http://bad%issuer"})
	_, _, err = service.deviceRequest(context.Background(), "token", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid URL escape") {
		t.Fatalf("device request: %v", err)
	}
}

func TestDeviceDecodeDiagnosticRetainsPosition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, `{"device_auth_id": !}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	service := New(Config{Issuer: server.URL})
	_, _, err := service.deviceRequest(context.Background(), "usercode", nil)
	var syntax *json.SyntaxError
	if err == nil || !errors.As(err, &syntax) || !strings.Contains(err.Error(), "byte 20") {
		t.Fatalf("device decode: %v", err)
	}
}

func TestTokenExchangeDiagnosticsRetainCauseAndProtectGrant(t *testing.T) {
	const grant = "fixture-refresh-grant"
	cause := fmt.Errorf("connection refused for %s", grant)
	service := New(Config{HTTPClient: &http.Client{Transport: diagnosticTransport(func(*http.Request) (*http.Response, error) { return nil, cause })}})
	_, err := service.tokenRequest(context.Background(), "chatgpt", url.Values{"refresh_token": {grant}})
	if err == nil || !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "rather than retrying") || strings.Contains(err.Error(), grant) || !errors.Is(err, cause) {
		t.Fatalf("token exchange diagnostic: %v", err)
	}
	service = New(Config{Issuer: "http://bad%issuer"})
	_, err = service.tokenRequest(context.Background(), "chatgpt", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid URL escape") {
		t.Fatalf("token request: %v", err)
	}
}

func TestTokenDecodeDiagnosticRetainsPosition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, `{"access_token": !}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	service := New(Config{Issuer: server.URL})
	_, err := service.tokenRequest(context.Background(), "chatgpt", nil)
	var syntax *json.SyntaxError
	if err == nil || !errors.As(err, &syntax) || !strings.Contains(err.Error(), "byte 18") {
		t.Fatalf("token decode: %v", err)
	}
}
