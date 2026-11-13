package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/port"
	"github.com/iamnalinor/qual/internal/session"
)

type batchProvider struct {
	calls  int
	cancel context.CancelFunc
	effect bool
}

func (*batchProvider) Models(context.Context) ([]domain.ModelInfo, error) { return nil, nil }
func (p *batchProvider) Run(_ context.Context, r domain.ModelRequest, emit func(domain.ModelEvent) error) error {
	p.calls++
	if p.cancel != nil {
		cancel := p.cancel
		p.cancel = nil
		cancel()
		return context.Canceled
	}
	var snapshot domain.Snapshot
	if err := json.Unmarshal([]byte(strings.TrimPrefix(r.Messages[1].Content, "Fixed initial snapshot: ")), &snapshot); err != nil {
		return err
	}
	role := "reviewer"
	if r.AgentID == "leader" {
		role = "leader"
	}
	files := []struct{ path, content string }{{"report.md", "# Actual review"}, {"findings.json", "[]"}, {"manifest.json", fmt.Sprintf(`{"run_id":%q,"diff_hash":%q}`, r.RunID, snapshot.DiffHash)}}
	if role == "leader" {
		files = append(files, struct{ path, content string }{"decisions.json", "[]"})
	}
	if p.effect && r.AgentID == "primary" {
		if err := call(emit, "effect", "fixture_effect", map[string]any{}); err != nil {
			return err
		}
	}
	for i, file := range files {
		if err := call(emit, fmt.Sprintf("write-%d", i), "write_file", map[string]any{"path": file.path, "content": file.content}); err != nil {
			return err
		}
	}
	args := map[string]any{"kind": role, "report_path": "report.md", "findings_path": "findings.json", "manifest_path": "manifest.json"}
	if role == "leader" {
		args["decisions_path"] = "decisions.json"
	}
	return call(emit, "finish", "finish", args)
}

func batchRuntime(t *testing.T, p *batchProvider) (*Runtime, []domain.AgentSpec) {
	t.Helper()
	home := t.TempDir()
	rt := New(Config{Home: home, Source: diskSource{root: filepath.Join(home, "work")}, Store: session.New(home), Providers: map[string]port.Provider{"fixture": p}, MaxTurns: 1})
	return rt, []domain.AgentSpec{{ID: "primary", Role: domain.RoleReviewer, Provider: "fixture"}, {ID: "leader", Role: domain.RoleLeader, Provider: "fixture"}}
}

func TestFinalAllowedResponseDrainsToolsThroughFinish(t *testing.T) {
	provider := &batchProvider{}
	rt, agents := batchRuntime(t, provider)
	state, err := rt.Review(context.Background(), domain.SourceRequest{RunID: "last-turn"}, agents)
	if err != nil || state.Status != domain.StatusCompleted || provider.calls != 2 {
		t.Fatalf("last turn lost its tools: %s, calls %d: %v", state.Status, provider.calls, err)
	}
}

func TestCancellationDuringModelResumesPrimary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &batchProvider{cancel: cancel}
	rt, agents := batchRuntime(t, provider)
	state, err := rt.Review(ctx, domain.SourceRequest{RunID: "cancel-model"}, agents)
	if !errors.Is(err, context.Canceled) || state.Status != domain.StatusCancelled {
		t.Fatalf("model cancel: %s %v", state.Status, err)
	}
	restored, err := rt.recover(context.Background(), state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if restored["primary"].Finished != nil {
		t.Fatal("cancelled primary became terminal")
	}
	state, err = rt.Resume(context.Background(), state.RunID)
	if err != nil || state.Status != domain.StatusCompleted || provider.calls != 3 {
		t.Fatalf("did not resume primary: %s calls %d %v", state.Status, provider.calls, err)
	}
	ledger, err := rt.Ledger(context.Background(), state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if ledger["primary"].Unknown != 2 {
		t.Fatalf("cancel/resume reset charge: %+v", ledger["primary"])
	}
}

type cancellingEffect struct {
	cancel context.CancelFunc
	path   string
	calls  int
}

func (*cancellingEffect) Definition() domain.ToolDefinition {
	return definition("fixture_effect", "Test filesystem effect", `{}`, nil, false)
}

func (t *cancellingEffect) Execute(_ context.Context, c domain.ToolCall) (domain.ToolResult, error) {
	t.calls++
	err := os.WriteFile(t.path, []byte("effect confirmed"), 0o600)
	t.cancel()
	return domain.ToolResult{CallID: c.ID, Content: "effect committed"}, err
}

func TestCancellationAfterToolIntentResumesWithoutRepeatingEffect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &batchProvider{effect: true}
	rt, agents := batchRuntime(t, provider)
	effect := &cancellingEffect{cancel: cancel, path: filepath.Join(t.TempDir(), "effect")}
	rt.config.Tools = []port.Tool{effect}
	state, err := rt.Review(ctx, domain.SourceRequest{RunID: "cancel-tool"}, agents)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("tool cancel: %v", err)
	}
	state, err = rt.Resume(context.Background(), state.RunID)
	if err != nil || state.Status != domain.StatusCompleted || effect.calls != 1 {
		t.Fatalf("confirmed effect lost/repeated: %s calls %d %v", state.Status, effect.calls, err)
	}
}

type transientStaleSource struct {
	diskSource
	checks int
}

func (s *transientStaleSource) Stale(context.Context, domain.Snapshot) (bool, error) {
	s.checks++
	if s.checks == 1 {
		return false, fmt.Errorf("transient source verification failure")
	}
	return false, nil
}

func TestSourceVerificationFailureIsIncompleteUntilResume(t *testing.T) {
	provider := &batchProvider{}
	rt, agents := batchRuntime(t, provider)
	disk, ok := rt.config.Source.(diskSource)
	if !ok {
		t.Fatal("fixture source changed")
	}
	source := &transientStaleSource{diskSource: disk}
	rt.config.Source = source
	rt.config.MaxTurns = 2
	state, err := rt.Review(context.Background(), domain.SourceRequest{RunID: "verify"}, agents)
	if err == nil || state.Status == domain.StatusCompleted {
		t.Fatalf("unverified run completed: %s %v", state.Status, err)
	}
	before := provider.calls
	state, err = rt.Resume(context.Background(), state.RunID)
	if err != nil || state.Status != domain.StatusCompleted || source.checks != 2 || provider.calls != before {
		t.Fatalf("resume skipped verification or repeated effects: %s checks %d calls %d %v", state.Status, source.checks, provider.calls, err)
	}
}

type metadataTool struct{ result domain.ToolResult }

func (*metadataTool) Definition() domain.ToolDefinition {
	return definition("metadata", "Test protocol error and artifact metadata", `{}`, nil, true)
}

func (t *metadataTool) Execute(context.Context, domain.ToolCall) (domain.ToolResult, error) {
	return t.result, nil
}

func TestRegisteredToolMetadataSurvivesJournalAndTranscript(t *testing.T) {
	rt, state, p := requestFixture(t, &pricedProvider{})
	ctx := context.Background()
	want := domain.ToolResult{CallID: "metadata-call", Content: "remote protocol error", IsError: true, ArtifactID: "upstream-artifact", Truncated: true}
	rt.config.Tools = []port.Tool{&metadataTool{result: want}}
	p.Pending = []domain.ToolCall{{ID: want.CallID, Name: "metadata", Arguments: json.RawMessage(`{}`)}}
	if err := rt.tools(ctx, &state, p); err != nil {
		t.Fatal(err)
	}
	var got domain.ToolResult
	if err := json.Unmarshal([]byte(p.Messages[len(p.Messages)-1].Content), &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("tool metadata discarded: got %+v want %+v", got, want)
	}
	restored, err := rt.recover(ctx, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if restored[p.Spec.ID].Outcomes[want.CallID].Result != want {
		t.Fatal("journal discarded tool metadata")
	}
}

func TestCompactionCountsToolArgumentsAndProviderData(t *testing.T) {
	provider := &pricedProvider{}
	rt, state, p := requestFixture(t, provider)
	rt.config.CompactThreshold = 10000
	p.Messages = append(p.Messages, domain.Message{Role: "assistant", ToolCalls: []domain.ToolCall{{ID: "large", Name: "write_file", Arguments: json.RawMessage(`{"path":"large","content":"` + strings.Repeat("x", 15000) + `"}`)}}, ProviderData: json.RawMessage(`{"opaque":"` + strings.Repeat("y", 15000) + `"}`)}, domain.Message{Role: "tool", ToolCallID: "large", Content: "written"})
	if err := rt.compact(context.Background(), &state, p); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || len(p.Messages) != 3 || !strings.Contains(p.Messages[2].Content, "full transcript artifact") {
		t.Fatalf("large structured context was not compacted: calls %d messages %d", provider.calls, len(p.Messages))
	}
}

func TestTruncatingRegisteredResultPreservesUpstreamArtifact(t *testing.T) {
	rt, state, p := requestFixture(t, &pricedProvider{})
	rt.config.MaxOutputBytes = 256
	rt.config.Tools = []port.Tool{&metadataTool{result: domain.ToolResult{Content: strings.Repeat("e", 1000), ArtifactID: "upstream-evidence", IsError: true, Truncated: true}}}
	outcome := rt.executeTool(context.Background(), &state, p, domain.ToolCall{ID: "large-metadata", Name: "metadata", Arguments: json.RawMessage(`{}`)})
	if !outcome.Result.Truncated || !outcome.Result.IsError || outcome.Result.ArtifactID != "upstream-evidence" {
		t.Fatalf("truncation erased registered metadata: %+v", outcome.Result)
	}
}
