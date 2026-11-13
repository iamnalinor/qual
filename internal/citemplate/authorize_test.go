package citemplate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, event, action, body, label, permission string
		draft, trusted, want                         bool
	}{
		{name: "opened", event: "pull_request_target", action: "opened", permission: "write", trusted: true, want: true},
		{name: "ready", event: "pull_request_target", action: "ready_for_review", permission: "maintain", trusted: true, want: true},
		{name: "updated", event: "pull_request_target", action: "synchronize", permission: "admin", trusted: true, want: true},
		{name: "label", event: "pull_request_target", action: "labeled", label: "qual-review", permission: "write", trusted: true, want: true},
		{name: "other label", event: "pull_request_target", action: "labeled", label: "review", permission: "write", trusted: true},
		{name: "comment", event: "issue_comment", action: "created", body: "/qual review", permission: "write", trusted: true, want: true},
		{name: "injected comment", event: "issue_comment", action: "created", body: "/qual review; echo bad", permission: "write", trusted: true},
		{name: "reader", event: "pull_request_target", action: "opened", permission: "read", trusted: true},
		{name: "draft", event: "pull_request_target", action: "opened", permission: "write", draft: true, trusted: true},
		{name: "untrusted workflow", event: "pull_request", action: "opened", permission: "admin", trusted: false},
		{name: "closed", event: "pull_request_target", action: "closed", permission: "write", trusted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkFixtureToken(t, r)
				switch r.URL.Path {
				case "/repos/org/repo/pulls/7":
					writeFixture(t, w, fmt.Sprintf(`{"number":7,"state":"open","draft":%t,"html_url":"https://github.com/org/repo/pull/7","base":{"sha":"%s","repo":{"full_name":"org/repo"}},"head":{"sha":"%s"}}`, tc.draft, strings.Repeat("a", 40), strings.Repeat("b", 40)))
				case "/repos/org/repo/collaborators/alice/permission":
					writeFixture(t, w, fmt.Sprintf(`{"permission":%q}`, tc.permission))
				default:
					t.Errorf("unexpected API request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			payload := fmt.Sprintf(`{"action":%q,"repository":{"full_name":"org/repo"},"sender":{"login":"alice"},"number":7,"label":{"name":%q},"issue":{"number":7,"pull_request":{}},"comment":{"body":%q}}`, tc.action, tc.label, tc.body)
			d, err := Authorize(context.Background(), Input{Provider: "github", Event: tc.event, Payload: []byte(payload), Repository: "org/repo", APIURL: server.URL, Token: "fixture-token", TrustedSource: tc.trusted}, server.Client())
			if (err == nil) != tc.want {
				t.Fatalf("allowed=%t err=%v", tc.want, err)
			}
			if tc.want && (d.HeadSHA != strings.Repeat("b", 40) || d.TargetSHA != strings.Repeat("a", 40) || d.Actor != "alice") {
				t.Fatalf("bad decision: %+v", d)
			}
		})
	}
}

func TestGenerateRejectsUnknownProvider(t *testing.T) {
	if _, err := Generate("unknown"); err == nil {
		t.Fatal("expected unsupported provider")
	}
}

func writeFixture(t *testing.T, w http.ResponseWriter, value string) {
	t.Helper()
	if _, err := fmt.Fprint(w, value); err != nil {
		t.Errorf("write fixture: %v", err)
	}
}

func checkFixtureToken(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer fixture-token" {
		t.Error("missing API authorization")
	}
}
