package citemplate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitLabAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, kind, action, note string
		level                    int
		draft, want              bool
	}{
		{name: "open", kind: "merge_request", action: "open", level: 40, want: true},
		{name: "update", kind: "merge_request", action: "update", level: 30, want: true},
		{name: "command", kind: "note", note: "/qual review", level: 40, want: true},
		{name: "injected command", kind: "note", note: "/qual review\nmalicious", level: 40},
		{name: "reader", kind: "merge_request", action: "open", level: 20},
		{name: "draft", kind: "merge_request", action: "open", level: 40, draft: true},
		{name: "unknown event", kind: "pipeline", level: 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/projects/19/merge_requests/7":
					writeFixture(t, w, fmt.Sprintf(`{"iid":7,"project_id":19,"state":"opened","draft":%t,"web_url":"https://gitlab.example/org/repo/-/merge_requests/7","sha":"%s","target_branch":"main"}`, tc.draft, strings.Repeat("b", 40)))
				case "/projects/19/repository/branches/main":
					writeFixture(t, w, fmt.Sprintf(`{"commit":{"id":"%s"}}`, strings.Repeat("a", 40)))
				case "/projects/19/members/all/3":
					writeFixture(t, w, fmt.Sprintf(`{"id":3,"access_level":%d}`, tc.level))
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			payload := fmt.Sprintf(`{"object_kind":%q,"project":{"id":19},"user":{"id":3},"object_attributes":{"iid":7,"action":%q,"note":%q,"noteable_type":"MergeRequest"},"merge_request":{"iid":7}}`, tc.kind, tc.action, tc.note)
			d, err := Authorize(context.Background(), Input{Provider: "gitlab", Event: "trigger", Payload: []byte(payload), Repository: "19", APIURL: server.URL, Token: "fixture", TrustedSource: true}, server.Client())
			if (err == nil) != tc.want {
				t.Fatalf("want=%t err=%v", tc.want, err)
			}
			if tc.want && (d.HeadSHA != strings.Repeat("b", 40) || d.TargetSHA != strings.Repeat("a", 40)) {
				t.Fatalf("bad snapshot %+v", d)
			}
		})
	}
}

func TestGitLabActiveExpiringMembership(t *testing.T) {
	for _, tc := range []struct {
		expiry string
		want   bool
	}{{"2999-01-01", true}, {"2000-01-01", false}, {"invalid", false}} {
		t.Run(tc.expiry, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeFixture(t, w, fmt.Sprintf(`{"id":3,"access_level":40,"expires_at":%q}`, tc.expiry))
			}))
			defer server.Close()
			err := gitlabPermission(context.Background(), Input{APIURL: server.URL, Repository: "19"}, server.Client(), "3", 3)
			if (err == nil) != tc.want {
				t.Fatalf("want active=%t err=%v", tc.want, err)
			}
		})
	}
}
