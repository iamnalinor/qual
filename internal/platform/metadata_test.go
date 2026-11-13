package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitLabNestedForkAndHostCredential(t *testing.T) {
	h := strings.Repeat("a", 40)
	targetSHA := strings.Repeat("b", 40)
	server := httptest.NewTLSServer(gitlabHandler(t, h, targetSHA))
	defer server.Close()
	source := New(Config{HTTPClient: server.Client(), APIBaseURLs: map[string]string{"git.example": server.URL}, Tokens: map[string]string{"git.example": "gitlab-private", "github.com": "other-private"}})
	target, err := ParseTarget("https://git.example/group/nested/repo/-/merge_requests/8", []string{"git.example"})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := source.metadata(context.Background(), target, true)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Head != h || metadata.TargetSHA != targetSHA || metadata.HeadRepository != "fork/repo" || len(metadata.Items["discussions"]) != 2 {
		t.Fatalf("metadata %+v", metadata)
	}
}

func TestCheckRunsPagination(t *testing.T) {
	h := strings.Repeat("a", 40)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/1"):
			writeResponse(t, w, []byte(`{"number":1,"head":{"sha":"`+h+`","repo":{"full_name":"o/r","clone_url":"https://github.com/o/r.git"}},"base":{"sha":"`+h+`","repo":{"full_name":"o/r","clone_url":"https://github.com/o/r.git"}}}`))
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", `<?page=2>; rel="next"`)
			}
			writeResponse(t, w, []byte(`{"check_runs":[{"id":1}]}`))
		default:
			writeResponse(t, w, []byte(`[]`))
		}
	}))
	defer server.Close()
	source := New(Config{HTTPClient: server.Client(), APIBaseURLs: map[string]string{"github.com": server.URL}})
	target := testTarget(t, "https://github.com/o/r/pull/1")
	metadata, err := source.metadata(context.Background(), target, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.Items["checks"]) != 2 {
		t.Fatalf("checks pages: %d", len(metadata.Items["checks"]))
	}
}

func TestInaccessibleSource(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	source := New(Config{HTTPClient: server.Client(), APIBaseURLs: map[string]string{"github.com": server.URL}})
	target := testTarget(t, "https://github.com/o/r/pull/1")
	if _, err := source.metadata(context.Background(), target, true); err == nil {
		t.Fatal("unavailable source accepted")
	}
}

func TestMetadataRejectsRepositoryURLMismatch(t *testing.T) {
	h := strings.Repeat("a", 40)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(t, w, []byte(`{"number":1,"head":{"sha":"`+h+`","repo":{"full_name":"fork/r","clone_url":"https://github.com/other/r.git"}},"base":{"sha":"`+h+`","repo":{"full_name":"o/r","clone_url":"https://github.com/o/r.git"}}}`))
	}))
	defer server.Close()
	source := New(Config{HTTPClient: server.Client(), APIBaseURLs: map[string]string{"github.com": server.URL}})
	if _, err := source.metadata(context.Background(), testTarget(t, "https://github.com/o/r/pull/1"), false); err == nil {
		t.Fatal("mismatched fork repository URL accepted")
	}
}

func gitlabHandler(t *testing.T, h, targetSHA string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "gitlab-private" || r.Header.Get("Authorization") != "" {
			t.Error("wrong credential route")
		}
		var body string
		switch r.URL.Path {
		case "/projects/group/nested/repo":
			body = `{"id":10,"path_with_namespace":"group/nested/repo","http_url_to_repo":"https://git.example/group/nested/repo.git"}`
		case "/projects/group/nested/repo/merge_requests/8":
			body = `{"iid":8,"project_id":10,"source_project_id":20,"sha":"` + h + `","source_branch":"feature","target_branch":"release/main"}`
		case "/projects/20":
			body = `{"id":20,"path_with_namespace":"fork/repo","http_url_to_repo":"https://git.example/fork/repo.git"}`
		case "/projects/group/nested/repo/repository/branches/release/main":
			body = `{"commit":{"id":"` + targetSHA + `"}}`
		default:
			body = `[{"id":1}]`
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("X-Next-Page", "2")
			}
		}
		writeResponse(t, w, []byte(body))
	}
}
