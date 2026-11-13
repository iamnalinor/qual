package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/port"
	"github.com/iamnalinor/qual/internal/session"
)

type diskSource struct{ root string }

func (s diskSource) Prepare(_ context.Context, r domain.SourceRequest) (domain.Snapshot, error) {
	return domain.Snapshot{RunID: r.RunID, DiffHash: "fixed", HeadCommit: "right", Files: []domain.ChangedFile{{Path: "code.go"}}}, nil
}

func (s diskSource) Worktree(_ context.Context, _ domain.Snapshot, id domain.AgentID) (string, error) {
	p := filepath.Join(s.root, string(id))
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(p, "code.go"), []byte("package sample\n"), 0o600); err != nil {
		return "", err
	}
	return p, nil
}
func (diskSource) Stale(context.Context, domain.Snapshot) (bool, error) { return false, nil }

type scripted struct {
	t     *testing.T
	roles []domain.AgentID
	calls int
}

func (*scripted) Models(context.Context) ([]domain.ModelInfo, error) { return nil, nil }
func (p *scripted) Run(_ context.Context, r domain.ModelRequest, emit func(domain.ModelEvent) error) error {
	p.calls++
	if len(r.Messages) < 2 {
		return fmt.Errorf("missing initial contract")
	}
	var tools []domain.Message
	for _, m := range r.Messages {
		if m.Role == "tool" {
			tools = append(tools, m)
			if strings.Contains(m.Content, `"is_error":true`) {
				return fmt.Errorf("tool failed: %s", m.Content)
			}
		}
	}
	if len(tools) == 0 {
		p.roles = append(p.roles, r.AgentID)
		return call(emit, "inspect", "read_file", map[string]any{"path": "code.go"})
	}
	if len(tools) == 1 {
		return call(emit, "w1", "write_file", map[string]any{"path": "report.md", "content": "# Checked actual source\n" + tools[0].Content})
	}
	if len(tools) == 2 {
		return call(emit, "w2", "write_file", map[string]any{"path": "findings.json", "content": "[]"})
	}
	var snapshot domain.Snapshot
	if err := json.Unmarshal([]byte(strings.TrimPrefix(r.Messages[1].Content, "Fixed initial snapshot: ")), &snapshot); err != nil {
		return err
	}
	if len(tools) == 3 {
		return call(emit, "w3", "write_file", map[string]any{"path": "manifest.json", "content": fmt.Sprintf(`{"run_id":%q,"diff_hash":%q,"checked_files":["code.go"]}`, r.RunID, snapshot.DiffHash)})
	}
	return scriptedFinish(r, emit, tools)
}

func scriptedFinish(r domain.ModelRequest, emit func(domain.ModelEvent) error, tools []domain.Message) error {
	role := "reviewer"
	if r.AgentID == "leader" {
		role = "leader"
		if len(tools) == 4 {
			return call(emit, "w4", "write_file", map[string]any{"path": "decisions.json", "content": "[]"})
		}
	}
	if r.AgentID == "setup" {
		role = "setup"
		if len(tools) == 4 {
			return call(emit, "w4", "write_file", map[string]any{"path": "proposals.json", "content": `{"settings":{},"rules":[],"commands":[],"reasons":[],"questions":[]}`})
		}
	}
	args := map[string]any{"kind": role, "report_path": "report.md", "findings_path": "findings.json", "manifest_path": "manifest.json"}
	if role == "leader" {
		args["decisions_path"] = "decisions.json"
	}
	if role == "setup" {
		args["proposals_path"] = "proposals.json"
	}
	return call(emit, "f", "finish", args)
}

func call(emit func(domain.ModelEvent) error, id, name string, args any) error {
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return emit(domain.ModelEvent{Type: "tool_call", ToolCall: &domain.ToolCall{ID: id, Name: name, Arguments: data}})
}

func TestRealFinishRoleOrderingAndResume(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	p := &scripted{t: t}
	store := session.New(home)
	rt := New(Config{Home: home, Source: diskSource{root: filepath.Join(home, "work")}, Store: store, Providers: map[string]port.Provider{"fixture": p}})
	agents := []domain.AgentSpec{{ID: "leader", Role: domain.RoleLeader, Provider: "fixture"}, {ID: "primary", Role: domain.RoleReviewer, Provider: "fixture"}}
	state, err := rt.Review(ctx, domain.SourceRequest{RunID: "run"}, agents)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.StatusCompleted || len(state.Results) != 2 || p.roles[0] != "primary" || p.roles[1] != "leader" {
		t.Fatalf("state %+v roles %v", state, p.roles)
	}
	before := p.calls
	state, err = rt.Resume(ctx, "run")
	if err != nil || p.calls != before || state.Status != domain.StatusCompleted {
		t.Fatalf("replayed finished run: %v", err)
	}
	events, err := store.Events(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	verifyToolJournal(t, events)
}

func TestFinishRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := confined(root, "escape", false); err == nil {
		t.Fatal("accepted escaped symlink")
	}
}

func TestUnknownModifyingOutcomeStopsWithoutReplay(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	store := session.New(home)
	source := diskSource{root: filepath.Join(home, "work")}
	p := &scripted{t: t}
	rt := New(Config{Home: home, Source: source, Store: store, Providers: map[string]port.Provider{"fixture": p}})
	state := domain.RunState{SchemaVersion: 1, RunID: "unknown", Status: domain.StatusRunning, Snapshot: domain.Snapshot{RunID: "unknown", DiffHash: "fixed"}, Agents: []domain.AgentSpec{{ID: "primary", Role: domain.RoleSetup, Provider: "fixture"}}}
	if err := store.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	progress := &agentProgress{Spec: state.Agents[0]}
	if err := rt.initialize(ctx, &state, progress); err != nil {
		t.Fatal(err)
	}
	call := domain.ToolCall{ID: "uncertain", Name: "shell", Arguments: json.RawMessage(`{"command":"printf replayed > counter"}`)}
	progress.Pending = []domain.ToolCall{call}
	if err := rt.checkpoint(ctx, state.RunID, progress); err != nil {
		t.Fatal(err)
	}
	if err := rt.event(ctx, state.RunID, progress.Spec.ID, "tool_intent", call); err != nil {
		t.Fatal(err)
	}
	_, err := rt.Resume(ctx, state.RunID)
	if domain.CodeOf(err) != domain.ErrIncomplete {
		t.Fatalf("unknown write: %v", err)
	}
	if _, err = os.Stat(filepath.Join(progress.Workspace, "counter")); !os.IsNotExist(err) {
		t.Fatalf("unknown command replayed: %v", err)
	}
	if p.calls != 0 {
		t.Fatal("called model before resolving unknown write")
	}
}

func TestConfirmedResultRecoveredWithoutRepeatingWrite(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	store := session.New(home)
	source := diskSource{root: filepath.Join(home, "work")}
	rt := New(Config{Home: home, Source: source, Store: store})
	state := domain.RunState{SchemaVersion: 1, RunID: "confirmed", Snapshot: domain.Snapshot{RunID: "confirmed"}}
	if err := store.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	p := &agentProgress{Spec: domain.AgentSpec{ID: "primary"}}
	if err := rt.initialize(ctx, &state, p); err != nil {
		t.Fatal(err)
	}
	call := domain.ToolCall{ID: "written", Name: "write_file", Arguments: json.RawMessage(`{"path":"counter","content":"replayed"}`)}
	p.Pending = []domain.ToolCall{call}
	if err := rt.checkpoint(ctx, state.RunID, p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Workspace, "counter"), []byte("confirmed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rt.event(ctx, state.RunID, p.Spec.ID, "tool_intent", call); err != nil {
		t.Fatal(err)
	}
	if err := rt.event(ctx, state.RunID, p.Spec.ID, "tool_result", toolOutcome{Call: call, Result: domain.ToolResult{CallID: call.ID, Content: "written"}}); err != nil {
		t.Fatal(err)
	}
	restored, err := rt.recover(ctx, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.tools(ctx, &state, restored[p.Spec.ID]); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(p.Workspace, "counter"))
	if err != nil || string(content) != "confirmed" {
		t.Fatalf("confirmed effect replayed %s %v", content, err)
	}
}

func verifyToolJournal(t *testing.T, events []domain.Event) {
	t.Helper()
	intents := 0
	results := 0
	for _, e := range events {
		if e.Type == "tool_intent" {
			intents++
		}
		if e.Type == "tool_result" {
			results++
		}
	}
	if intents != results || results != 11 {
		t.Fatalf("journal effects %d/%d", intents, results)
	}
}

func TestPrimaryHardLimitStillRunsLeader(t *testing.T) {
	home := t.TempDir()
	p := &scripted{t: t}
	rt := New(Config{Home: home, Source: diskSource{root: filepath.Join(home, "work")}, Store: session.New(home), Providers: map[string]port.Provider{"fixture": p}})
	agents := []domain.AgentSpec{{ID: "primary", Role: domain.RoleReviewer, Provider: "fixture", Budget: domain.BudgetLimits{Hard: 1}}, {ID: "leader", Role: domain.RoleLeader, Provider: "fixture"}}
	state, err := rt.Review(context.Background(), domain.SourceRequest{RunID: "limited"}, agents)
	if err != nil || state.Status != domain.StatusCompleted || state.Results[0].Status != domain.StatusBudgetStopped {
		t.Fatalf("limited primary: %+v %v", state, err)
	}
	if len(p.roles) != 1 || p.roles[0] != "leader" {
		t.Fatalf("role execution %v", p.roles)
	}
	if len(state.Results[0].Artifacts) == 0 {
		t.Fatal("leader received no partial transcript artifact")
	}
}

func TestLeaderHardLimitNeverCompletesAndLedgerSurvivesResume(t *testing.T) {
	home := t.TempDir()
	p := &scripted{t: t}
	store := session.New(home)
	rt := New(Config{Home: home, Source: diskSource{root: filepath.Join(home, "work")}, Store: store, Providers: map[string]port.Provider{"fixture": p}})
	agents := []domain.AgentSpec{{ID: "primary", Role: domain.RoleReviewer, Provider: "fixture"}, {ID: "leader", Role: domain.RoleLeader, Provider: "fixture", Budget: domain.BudgetLimits{Hard: 1}}}
	state, err := rt.Review(context.Background(), domain.SourceRequest{RunID: "leader-limited"}, agents)
	if domain.CodeOf(err) != domain.ErrBudget || state.Status != domain.StatusBudgetStopped {
		t.Fatalf("leader limit: %s %v", state.Status, err)
	}
	balances, err := rt.Ledger(context.Background(), state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	before := p.calls
	resumed, resumeErr := rt.Resume(context.Background(), state.RunID)
	if domain.CodeOf(resumeErr) != domain.ErrBudget || resumed.Status == domain.StatusCompleted || p.calls != before {
		t.Fatalf("invalid resume: %s %v", resumed.Status, resumeErr)
	}
	after, err := rt.Ledger(context.Background(), state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if balances["primary"].Reserved != after["primary"].Reserved || after["primary"].Unknown == 0 {
		t.Fatal("resume lost unknown primary charges")
	}
}

func TestFinishRejectsNamedPipeBeforeOpening(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	rt := New(Config{})
	done := make(chan error, 1)
	go func() { _, err := rt.finishFile(root, "pipe"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted a named pipe")
		}
	case <-time.After(time.Second):
		t.Fatal("finish blocked opening a named pipe")
	}
}
