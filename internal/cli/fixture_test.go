package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

type fixtureRequest struct {
	Input []struct {
		Type, Role, Content, Name, Arguments, Output string
		CallID                                       string `json:"call_id"`
	} `json:"input"`
}

func fixture(t *testing.T, malformed ...bool) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"data":[{"id":"gpt-6-luna"}]}`))
			if err != nil {
				t.Error(err)
			}
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-only-credential" {
			t.Error("fixture provider received wrong credential")
		}
		calls.Add(1)
		var request fixtureRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		name, args, id := fixtureChoice(t, request, calls.Load(), malformed)
		w.Header().Set("Content-Type", "text/event-stream")
		encoded, err := json.Marshal(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": args}})
		if err != nil {
			t.Error(err)
			return
		}
		if _, err = fmt.Fprintf(w, "data: %s\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":20}}}\n\n", encoded); err != nil {
			t.Error(err)
		}
	}))
	return server, calls
}

func fixtureNext(t *testing.T, request fixtureRequest) (string, string, string) {
	t.Helper()
	snapshot, role, outputs, prior := inspectRequest(t, request)
	if _, read := outputs["read"]; !read {
		return "read_file", `{"path":"file.txt"}`, "read"
	}
	if !strings.Contains(outputs["read"], "initial") && !strings.Contains(outputs["read"], "changed") {
		t.Errorf("fixture must react to real tool output: %q", outputs["read"])
	}
	contents := []struct{ name, content string }{{"report.md", fixtureReport(request, outputs["read"])}, {"findings.json", "[]"}, {"manifest.json", fmt.Sprintf(`{"run_id":%q,"diff_hash":%q,"checked_files":["file.txt"],"evidence":[],"questions":[],"assumptions":[]}`, snapshot.RunID, snapshot.DiffHash)}}
	if role == "leader" {
		contents = append(contents, struct{ name, content string }{"decisions.json", "[]"})
	}
	if role == "setup" {
		contents = append(contents, struct{ name, content string }{"proposals.json", `{"settings":{"review_language":"fr"},"rules":["Check tests"],"commands":["go test ./..."],"reasons":["fixture"],"questions":[]}`})
	}
	for i, file := range contents {
		id := fmt.Sprintf("write-%d", i)
		if _, ok := outputs[id]; !ok {
			data, err := json.Marshal(map[string]string{"path": file.name, "content": file.content})
			if err != nil {
				t.Error(err)
			}
			return "write_file", string(data), id
		}
		if strings.Contains(outputs[id], `"is_error":true`) {
			t.Errorf("write failed %q (%s)", outputs[id], prior)
		}
	}
	return fixtureFinish(t, role)
}

func fixtureFinish(t *testing.T, role string) (string, string, string) {
	t.Helper()
	args := map[string]string{"kind": role, "report_path": "report.md", "findings_path": "findings.json", "manifest_path": "manifest.json"}
	if role == "leader" {
		args["decisions_path"] = "decisions.json"
	}
	if role == "setup" {
		args["proposals_path"] = "proposals.json"
	}
	data, err := json.Marshal(args)
	if err != nil {
		t.Error(err)
	}
	return "finish", string(data), "finish"
}

func TestExecutableRealResponsesReviewAndSetup(t *testing.T) {
	binary := buildCLI(t)
	server, calls := fixture(t)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	home := t.TempDir()
	repo := repository(t)
	configure(t, home, server.URL+"/v1")
	verifySetupAcceptance(t, binary, home, repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "file.txt")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed unstaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	out, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--staged", "--json", "--non-interactive", "--output", output, "--focus", "security", "--reasoning-effort", "low", "--instructions", "fixture-extra-instructions")
	if err != nil {
		t.Fatalf("review: %v %s %s", err, out, diagnostic)
	}
	var state domain.RunState
	if err = json.Unmarshal([]byte(out), &state); err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.StatusCompleted || len(state.Results) != 2 || !strings.Contains(state.Results[0].Report, "changed staged") || strings.Contains(state.Results[0].Report, "unstaged") {
		t.Fatalf("wrong review result: %+v", state)
	}
	verifyRoleOptions(t, state.Agents)
	if calls.Load() < 20 {
		t.Fatalf("runtime not exercised: %d calls", calls.Load())
	}
	verifyCompletedResume(t, binary, home, repo, state.RunID, out+diagnostic)
}

func inspectRequest(t *testing.T, request fixtureRequest) (snapshot domain.Snapshot, role string, outputs map[string]string, prior string) {
	t.Helper()
	role = "reviewer"
	outputs = map[string]string{}
	for _, item := range request.Input {
		if strings.Contains(item.Content, "Qual leader") {
			role = "leader"
		}
		if strings.Contains(item.Content, "Qual setup") {
			role = "setup"
		}
		snapshot = inspectSnapshot(t, item.Content, snapshot)
		if strings.HasPrefix(item.Content, "Saved primary results") {
			if !strings.Contains(item.Content, "fixture observed source") {
				t.Error("leader did not receive original reviewer report")
			}
		}
		if item.Type == "function_call" {
			prior = item.Name + ":" + item.Arguments
		}
		if item.Type == "function_call_output" {
			outputs[item.CallID] = item.Output
		}
	}
	return snapshot, role, outputs, prior
}

func verifySetupAcceptance(t *testing.T, binary, home, repo string) {
	t.Helper()
	for _, accept := range []bool{false, true} {
		args := []string{"setup", "--trust", "untrusted", "--non-interactive", "--json"}
		if accept {
			args = append(args, "--accept")
		}
		out, diagnostic, err := invoke(t, binary, home, repo, "", args...)
		if err != nil {
			t.Fatalf("setup: %v %s %s", err, out, diagnostic)
		}
		var state domain.RunState
		if err = json.Unmarshal([]byte(out), &state); err != nil {
			t.Fatal(err)
		}
		personal := filepath.Join(home, "projects", string(state.Snapshot.ProjectID), "settings.yaml")
		// #nosec G304 -- Personal path is generated from the fixture session and test-owned home.
		data, readErr := os.ReadFile(personal)
		if !accept && !os.IsNotExist(readErr) {
			t.Fatalf("rejected setup mutated personal configuration: %s %v", data, readErr)
		}
		if accept && (readErr != nil || !strings.Contains(string(data), "review_language: fr")) {
			t.Fatalf("accepted setup not saved: %s %v", data, readErr)
		}
	}
}

func verifyCompletedResume(t *testing.T, binary, home, repo string, id domain.RunID, visible string) {
	t.Helper()
	if strings.Contains(visible, "fixture-only-credential") {
		t.Fatal("credential leaked")
	}
	out, diagnostic, err := invoke(t, binary, home, repo, "", "sessions", "resume", string(id), "--json", "--non-interactive")
	if err != nil {
		t.Fatalf("resume: %v %s %s", err, out, diagnostic)
	}
}

func inspectSnapshot(t *testing.T, content string, previous domain.Snapshot) domain.Snapshot {
	t.Helper()
	value, ok := strings.CutPrefix(content, "Fixed initial snapshot: ")
	if !ok {
		return previous
	}
	var snapshot domain.Snapshot
	if err := json.Unmarshal([]byte(value), &snapshot); err != nil {
		t.Error(err)
	}
	return snapshot
}

func verifyRoleOptions(t *testing.T, agents []domain.AgentSpec) {
	t.Helper()
	if agents[0].Focus != "security" || agents[0].ReasoningEffort != "low" || !strings.Contains(agents[0].Instructions, "fixture-extra-instructions") || agents[1].Focus == "security" {
		t.Fatalf("independent reviewer settings lost: %+v", agents)
	}
}

func fixtureGoTool(t *testing.T, request fixtureRequest, name, args, id string) (string, string, string) {
	t.Helper()
	for _, item := range request.Input {
		if item.Type == "function_call_output" && item.CallID == "go-test" {
			if !strings.Contains(item.Output, "PASS") {
				t.Errorf("sandbox Go test failed: %s", item.Output)
			}
			return name, args, id
		}
	}
	return "shell", `{"command":"go test -v ./...","timeout_seconds":180}`, "go-test"
}

func fixtureChoice(t *testing.T, request fixtureRequest, call int64, flags []bool) (string, string, string) {
	t.Helper()
	name, args, id := fixtureNext(t, request)
	if len(flags) > 0 && flags[0] && call == 1 {
		return "finish", `{"kind":"reviewer","report_path":"missing.md","findings_path":"missing.json","manifest_path":"missing-manifest.json"}`, "invalid-finish"
	}
	if len(flags) > 1 && flags[1] {
		return fixtureGoTool(t, request, name, args, id)
	}
	if len(flags) > 2 && flags[2] {
		return fixtureQuestion(t, request, name, args, id)
	}
	return name, args, id
}

func fixtureQuestion(t *testing.T, request fixtureRequest, name, args, id string) (string, string, string) {
	t.Helper()
	russian := false
	for _, item := range request.Input {
		if item.Type == "function_call_output" && item.CallID == "question" {
			return name, args, id
		}
		if strings.Contains(item.Content, "Ask user questions in Russian (ru)") {
			russian = true
		}
	}
	if !russian {
		t.Error("question fixture received no Russian UI instruction")
	}
	_, role, _, _ := inspectRequest(t, request)
	return "ask_user", fmt.Sprintf(`{"question":"Какую проверку выполнить? (%s)"}`, role), "question"
}

func fixtureReport(request fixtureRequest, content string) string {
	for _, item := range request.Input {
		if strings.Contains(item.Content, "Output language: fr") {
			return "Rapport français. fixture observed source: " + content
		}
	}
	return "fixture observed source: " + content
}
