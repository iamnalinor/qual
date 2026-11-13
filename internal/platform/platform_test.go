package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseTargets(t *testing.T) {
	for _, tc := range []struct{ raw, repo, kind string }{
		{"https://github.com/org/repo/pull/42", "org/repo", "github"},
		{"https://gitlab.com/group/nested/repo/-/merge_requests/3", "group/nested/repo", "gitlab"},
		{"https://git.example/group/repo/-/merge_requests/1", "group/repo", "gitlab"},
	} {
		target, err := ParseTarget(tc.raw, []string{"git.example"})
		if err != nil || target.Repository != tc.repo || target.Kind != tc.kind {
			t.Fatalf("%s: %+v %v", tc.raw, target, err)
		}
	}
	for _, raw := range []string{"http://github.com/o/r/pull/1", "https://evil/o/r/pull/1", "https://token@github.com/o/r/pull/1", "https://github.com/o/r/pull/0", "https://github.com/o/r/pull/1?token=x", "https://github.com/o/r%3f/pull/1"} {
		if _, err := ParseTarget(raw, nil); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestPaginationAndCredentialBoundary(t *testing.T) {
	var foreignCalls int
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { foreignCalls++; w.WriteHeader(200) }))
	defer foreign.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-fixture" {
			t.Error("missing host credential")
		}
		if r.URL.Query().Get("page") == "2" {
			writeResponse(t, w, []byte(`[{"id":2}]`))
			return
		}
		if r.URL.Path == "/escape" {
			w.Header().Set("Link", "<"+foreign.URL+">; rel=\"next\"")
		} else {
			w.Header().Set("Link", "<?page=2>; rel=\"next\"")
		}
		writeResponse(t, w, []byte(`[{"id":1}]`))
	}))
	defer server.Close()
	source := New(Config{HTTPClient: server.Client(), Tokens: map[string]string{"github.com": "private-fixture"}})
	items, err := source.pages(context.Background(), server.URL+"/items", "github.com", "github")
	if err != nil || len(items) != 2 {
		t.Fatalf("pagination %d %v", len(items), err)
	}
	if _, err = source.pages(context.Background(), server.URL+"/escape", "github.com", "github"); err == nil || foreignCalls != 0 {
		t.Fatalf("cross origin: %v calls %d", err, foreignCalls)
	}
}
