package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBlockedRefreshDoesNotPreventCancellationOrOtherProfiles(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	service := New(Config{Home: t.TempDir(), Issuer: server.URL, HTTPClient: server.Client()})
	for _, id := range []string{"blocked", "other-token", "other-logout"} {
		expiry := time.Now().Add(time.Hour)
		if id == "blocked" {
			expiry = time.Now().Add(-time.Hour)
		}
		c := Credentials{ID: id, Kind: "chatgpt", ClientID: "issued", Subject: id, AccessToken: "fixture", RefreshToken: "rotating", Scopes: []string{SharingScope}, ExpiresAt: expiry}
		if err := service.Save(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	first := make(chan error, 1)
	go func() { _, err := service.TokenSource("blocked").Token(t.Context()); first <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	operations := []struct {
		name      string
		cancelled bool
		run       func(context.Context) error
	}{
		{"same profile token", true, func(ctx context.Context) error { _, err := service.TokenSource("blocked").Token(ctx); return err }},
		{"same profile logout", true, func(ctx context.Context) error { return service.Logout(ctx, "blocked") }},
		{"other profile token", false, func(ctx context.Context) error { _, err := service.TokenSource("other-token").Token(ctx); return err }},
		{"other profile logout", false, func(ctx context.Context) error { return service.Logout(ctx, "other-logout") }},
	}
	var pending []<-chan error
	for _, op := range operations {
		pending = append(pending, assertAuthOperationCompletes(t, op.name, op.cancelled, op.run))
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-first; err == nil {
		t.Error("failed refresh unexpectedly succeeded")
	}
	drainAuthOperations(t, pending)
}

func drainAuthOperations(t *testing.T, pending []<-chan error) {
	t.Helper()
	for _, result := range pending {
		if result == nil {
			continue
		}
		select {
		case err := <-result:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("timed out operation returned %v", err)
			}
		case <-time.After(time.Second):
			t.Error("operation remained blocked after release")
		}
	}
}

func assertAuthOperationCompletes(t *testing.T, name string, cancelled bool, run func(context.Context) error) <-chan error {
	t.Helper()
	timeout := 100 * time.Millisecond
	if !cancelled {
		timeout = time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- run(ctx) }()
	select {
	case err := <-result:
		if cancelled && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: expected deadline, got %v", name, err)
		}
		if !cancelled && err != nil {
			t.Errorf("%s blocked by unrelated refresh: %v", name, err)
		}
		return nil
	case <-time.After(2 * time.Second):
		t.Errorf("%s ignored cancellation/profile independence", name)
		return result
	}
}
