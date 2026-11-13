package citemplate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMalformedContextsNeverReachAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unauthorized context reached API")
		w.WriteHeader(500)
	}))
	defer server.Close()
	for _, tc := range []struct{ name, provider, event, repo, payload string }{
		{"ordinary PR", "github", "pull_request", "org/repo", `{"repository":{"full_name":"org/repo"},"action":"opened","number":7,"sender":{"login":"alice"},"trusted_source":true}`},
		{"foreign repository", "github", "pull_request_target", "org/repo", `{"repository":{"full_name":"evil/repo"},"action":"opened","number":7,"sender":{"login":"alice"}}`},
		{"issue comment", "github", "issue_comment", "org/repo", `{"repository":{"full_name":"org/repo"},"action":"created","sender":{"login":"alice"},"issue":{"number":7},"comment":{"body":"/qual review"}}`},
		{"malformed JSON", "github", "pull_request_target", "org/repo", `{`},
		{"MR pipeline", "gitlab", "merge_request_event", "19", `{"project":{"id":19}}`},
		{"foreign project", "gitlab", "trigger", "19", `{"project":{"id":20},"object_kind":"merge_request","object_attributes":{"iid":7,"action":"open"},"user":{"id":3}}`},
		{"zero actor", "gitlab", "trigger", "19", `{"project":{"id":19},"object_kind":"merge_request","object_attributes":{"iid":7,"action":"open"},"user":{"id":0}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Authorize(context.Background(), Input{Provider: tc.provider, Event: tc.event, Repository: tc.repo, Payload: []byte(tc.payload), APIURL: server.URL, TrustedSource: true}, server.Client())
			if err == nil {
				t.Fatal("expected fail closed")
			}
		})
	}
}

func TestGitHubRejectsUnusableMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, state, repo, sha string
		number                 int
	}{
		{"closed", "closed", "org/repo", strings.Repeat("a", 40), 7},
		{"wrong repository", "open", "other/repo", strings.Repeat("a", 40), 7},
		{"wrong number", "open", "org/repo", strings.Repeat("a", 40), 8},
		{"missing SHA", "open", "org/repo", "", 7},
		{"invalid SHA length", "open", "org/repo", strings.Repeat("a", 41), 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/permission") {
					writeFixture(t, w, `{"permission":"admin"}`)
					return
				}
				writeFixture(t, w, fmt.Sprintf(`{"number":%d,"state":%q,"html_url":"https://github.com/org/repo/pull/7","base":{"sha":%q,"repo":{"full_name":%q}},"head":{"sha":%q}}`, tc.number, tc.state, tc.sha, tc.repo, strings.Repeat("b", 40)))
			}))
			defer server.Close()
			_, err := Authorize(context.Background(), Input{Provider: "github", Event: "workflow_dispatch", ManualNumber: 7, Actor: "alice", Repository: "org/repo", Payload: []byte(`{"repository":{"full_name":"org/repo"}}`), APIURL: server.URL, TrustedSource: true}, server.Client())
			if err == nil {
				t.Fatal("expected metadata rejection")
			}
		})
	}
}

func TestManualActorsAndHTTPFailures(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests++; w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	_, err := Authorize(context.Background(), Input{Provider: "gitlab", Event: "web", ManualNumber: 7, Actor: "3", Repository: "19", Payload: []byte(`{}`), APIURL: server.URL, TrustedSource: true}, server.Client())
	if err == nil || !strings.HasPrefix(err.Error(), "ci_api:") || requests != 1 {
		t.Fatalf("API failure lost: %v requests=%d", err, requests)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Authorize(ctx, Input{Provider: "gitlab", Event: "web", ManualNumber: 7, Actor: "3", Repository: "19", APIURL: server.URL, TrustedSource: true}, server.Client())
	if err == nil || requests != 1 {
		t.Fatalf("cancelled authorization executed: %v", err)
	}
}

func TestGitLabLabels(t *testing.T) {
	for _, tc := range []struct {
		name, changes string
		want          bool
	}{
		{"new exact label", `{"labels":{"previous":[],"current":[{"title":"qual-review"}]}}`, true},
		{"other label", `{"labels":{"previous":[],"current":[{"title":"qual-review-extra"}]}}`, false},
		{"already present", `{"labels":{"previous":[{"title":"qual-review"}],"current":[{"title":"qual-review"}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"object_kind":"merge_request","project":{"id":19},"user":{"id":3},"object_attributes":{"iid":7,"action":"update"},"changes":%s}`, tc.changes)
			number, actor, err := gitlabTrigger(Input{Event: "trigger", Repository: "19", Payload: []byte(raw)})
			if (err == nil) != tc.want {
				t.Fatalf("want=%t err=%v", tc.want, err)
			}
			if tc.want && (number != 7 || actor != "3") {
				t.Fatalf("bad actor/number: %d %s", number, actor)
			}
		})
	}
}
