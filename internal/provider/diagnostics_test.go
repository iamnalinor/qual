package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

//nolint:gocognit,gocyclo // Each real transport failure checks actionable text and preserved machine semantics.
func TestTransportDiagnosticsFromRealHTTP(t *testing.T) {
	var listen net.ListenConfig
	listener, err := listen.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedURL := "http://" + listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	tlsServer := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	tlsServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsServer.StartTLS()
	defer tlsServer.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer slow.Close()
	for _, tc := range []struct {
		name, endpoint, want string
		timeout              time.Duration
	}{
		{"refused", refusedURL, "connection refused", time.Second},
		{"tls", tlsServer.URL, "certificate", time.Second},
		{"timeout", slow.URL, "deadline exceeded", 20 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(Config{Kind: "openai-compatible", BaseURL: tc.endpoint, Model: "fixture", HTTPClient: &http.Client{Timeout: tc.timeout}})
			if err != nil {
				t.Fatal(err)
			}
			err = p.Run(context.Background(), domain.ModelRequest{}, func(domain.ModelEvent) error { return nil })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("missing actionable cause: %v", err)
			}
			var coded *domain.Error
			if !errors.As(err, &coded) || coded.Code != "provider_transport" {
				t.Fatalf("code: %v", err)
			}
			if tc.name == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost deadline: %v", err)
			}
		})
	}
}

func TestTransportRedactsSelectedCredentialsAndURLs(t *testing.T) {
	const key = "selected-private-key"
	cause := fmt.Errorf("proxy denied %s https://user:password@proxy.test/token-path?access_token=query-secret\n\x1b[31m", key)
	p, err := New(Config{Kind: "openai", BaseURL: "http://example.test", APIKey: key, Model: "fixture", HTTPClient: &http.Client{Transport: diagnosticTransport(func(*http.Request) (*http.Response, error) { return nil, cause })}})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Run(context.Background(), domain.ModelRequest{}, func(domain.ModelEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "proxy denied") || !errors.Is(err, cause) {
		t.Fatalf("lost useful custom cause: %v", err)
	}
	for _, secret := range []string{key, "password", "token-path", "query-secret", "\n", "\x1b"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe diagnostic: %q", err.Error())
		}
	}
}

func TestCatalogDecodeDiagnosticRetainsPositionAndType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, `{"models": !}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	p, err := New(Config{Kind: "chatgpt", BaseURL: server.URL, TokenSource: TokenSourceFunc(func(context.Context) (Credential, error) { return Credential{AccessToken: "fixture-token"}, nil })})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Models(context.Background())
	var syntax *json.SyntaxError
	if err == nil || !errors.As(err, &syntax) || !strings.Contains(err.Error(), "byte 12") || !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("catalog diagnostic: %v", err)
	}
}

func TestProviderRequestConstructionDiagnostic(t *testing.T) {
	p := &adapter{config: Config{Kind: "openai", BaseURL: "http://bad%host"}}
	_, err := p.httpRequest(context.Background(), domain.ModelRequest{Model: "fixture"}, Credential{})
	if err == nil || !strings.Contains(err.Error(), "invalid URL escape") {
		t.Fatalf("request diagnostic: %v", err)
	}
}

func TestTransportCancellationAndRetryClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := transportError(ctx, context.Canceled)
	if !errors.Is(err, context.Canceled) || domain.CodeOf(err) != domain.ErrCancelled {
		t.Fatalf("cancelled: %v", err)
	}
	dns := &net.DNSError{Err: "no such host", Name: "missing.test", IsNotFound: true}
	err = transportError(context.Background(), dns)
	var coded *domain.Error
	var network *net.DNSError
	if !errors.As(err, &coded) || coded.Retryable || !errors.As(err, &network) {
		t.Fatalf("permanent DNS incorrectly retried: %v", err)
	}
}

type failingDiagnosticReader struct{ cause error }

func (r failingDiagnosticReader) Read([]byte) (int, error) { return 0, r.cause }

func TestStreamReadDiagnosticRetainsSafeCause(t *testing.T) {
	cause := errors.New("connection reset for fixture-secret")
	p, err := New(Config{Kind: "openai", BaseURL: "http://example.test", APIKey: "fixture-secret", Model: "fixture", HTTPClient: &http.Client{Transport: diagnosticTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(failingDiagnosticReader{cause: cause})}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Run(context.Background(), domain.ModelRequest{}, func(domain.ModelEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), "fixture-secret") || !errors.Is(err, cause) {
		t.Fatalf("stream read: %v", err)
	}
}
