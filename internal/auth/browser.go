package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// BrowserLogin starts a bounded loopback listener, then invokes the application's
// browser opener. The opener receives only the generated authorization URL.
func (s *Service) BrowserLogin(ctx context.Context, options LoginOptions, open func(string) error) (result Credentials, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	address := "127.0.0.1:0"
	if options.Kind == "codex" {
		address = "127.0.0.1:1455"
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return Credentials{}, fmt.Errorf("listen for OAuth callback: %w", err)
	}
	options.RedirectURI = "http://" + listener.Addr().String() + "/auth/callback"
	if options.Kind == "codex" {
		options.RedirectURI = "http://localhost:1455/auth/callback"
	}
	attempt, err := s.Begin(ctx, options)
	if err != nil {
		if closeErr := listener.Close(); closeErr != nil {
			return Credentials{}, fmt.Errorf("close OAuth listener: %w", closeErr)
		}
		return Credentials{}, err
	}
	callbacks := make(chan url.Values, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: callbackHandler(attempt, callbacks)}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	defer func() { resultErr = errors.Join(resultErr, server.Close()) }()
	if err = openAuthorization(open, attempt.URL); err != nil {
		return Credentials{}, err
	}
	select {
	case query := <-callbacks:
		return attempt.Complete(ctx, query)
	case <-ctx.Done():
		return Credentials{}, ctx.Err()
	case serveErr := <-serveErrors:
		if serveErr != nil {
			return Credentials{}, fmt.Errorf("OAuth callback listener: %w", serveErr)
		}
		return Credentials{}, authError("OAuth callback listener stopped")
	}
}

func callbackHandler(attempt *Attempt, callbacks chan<- url.Values) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.Method != http.MethodGet || r.URL.Path != "/auth/callback" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("state") != attempt.state {
			http.Error(w, "Invalid authorization state", http.StatusBadRequest)
			return
		}
		select {
		case callbacks <- r.URL.Query():
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "Authorization already received", http.StatusConflict)
		}
	})
}

func openAuthorization(open func(string) error, endpoint string) error {
	if open == nil {
		return authError("browser opener is required")
	}
	if err := open(endpoint); err != nil {
		return fmt.Errorf("open authorization browser: %w", err)
	}
	return nil
}
