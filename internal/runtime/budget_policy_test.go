package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/budget"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/port"
)

type budgetProvider struct {
	costs []domain.Money
	calls int
	tools []domain.ToolDefinition
	deny  bool
}

func (*budgetProvider) Models(context.Context) ([]domain.ModelInfo, error) { return nil, nil }
func (p *budgetProvider) Run(_ context.Context, request domain.ModelRequest, emit func(domain.ModelEvent) error) error {
	p.tools = request.Tools
	cost := domain.Money(0)
	if p.calls < len(p.costs) {
		cost = p.costs[p.calls]
	}
	p.calls++
	usage := domain.Usage{Known: true, CostKnown: true, Cost: cost}
	if err := emit(domain.ModelEvent{Type: "usage", Usage: &usage}); err != nil {
		return err
	}
	if p.deny {
		if err := call(emit, "denied-shell", "shell", map[string]any{"command": "exit 47"}); err != nil {
			return err
		}
		return call(emit, "denied-extension", "metadata", map[string]any{})
	}
	return emit(domain.ModelEvent{Type: "text", Text: "Continue with existing evidence."})
}

func budgetFixture(t *testing.T, limits domain.BudgetLimits, provider *budgetProvider) (*Runtime, domain.RunState, *agentProgress) {
	t.Helper()
	rt, state, p := requestFixture(t, provider)
	p.Spec.Budget = limits
	p.Ledger = budget.New(limits).Snapshot()
	p.Messages = nil
	if err := rt.initialize(context.Background(), &state, p); err != nil {
		t.Fatal(err)
	}
	return rt, state, p
}

func TestSoftBudgetGuidanceThresholdsAndReservePersist(t *testing.T) {
	provider := &budgetProvider{costs: []domain.Money{8000000, 2000000}}
	rt, state, p := budgetFixture(t, domain.BudgetLimits{Soft: 10000000, Hard: 20000000}, provider)
	ctx := context.Background()
	if !strings.Contains(p.Messages[0].Content, "budget_status") {
		t.Fatal("soft-budget agent received no monitoring guidance")
	}
	var noticeTypes []string
	rt.config.OnEvent = func(event domain.Event) {
		noticeTypes = append(noticeTypes, event.Type)
	}
	if err := rt.turn(ctx, &state, p); err != nil {
		t.Fatal(err)
	}
	if err := rt.turn(ctx, &state, p); err != nil {
		t.Fatal(err)
	}
	result := rt.executeTool(ctx, &state, p, domain.ToolCall{ID: "budget", Name: "budget_status", Arguments: json.RawMessage(`{}`)})
	var status BudgetStatus
	if err := json.Unmarshal([]byte(result.Result.Content), &status); err != nil {
		t.Fatal(err)
	}
	verifySoftStatus(t, status)
	restored, err := rt.recover(ctx, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.turn(ctx, &state, restored[p.Spec.ID]); err != nil {
		t.Fatal(err)
	}
	events, err := rt.config.Store.Events(ctx, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	verifyBudgetWarnings(t, noticeTypes, events, restored[p.Spec.ID].Phase)
}

func TestCompletionReserveRestrictsProviderAndDispatch(t *testing.T) {
	provider := &budgetProvider{deny: true}
	rt, state, p := budgetFixture(t, domain.BudgetLimits{Soft: 11000000, Hard: 12000000}, provider)
	p.Ledger.Spent = 10500000
	extension := &metadataTool{result: domain.ToolResult{Content: "should never run"}}
	rt.config.Tools = []port.Tool{extension}
	ctx := context.Background()
	if err := rt.turn(ctx, &state, p); err != nil {
		t.Fatal(err)
	}
	if p.Phase != "finalization" {
		t.Fatalf("investigation consumed completion reserve: %s", p.Phase)
	}
	verifyFinalizationTools(t, provider.tools)
	if err := rt.tools(ctx, &state, p); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"denied-shell", "denied-extension"} {
		outcome := p.Outcomes[id]
		if !outcome.Result.IsError || !strings.Contains(outcome.Result.Content, "finalization") {
			t.Fatalf("unadvertised effect executed: %+v", outcome)
		}
	}
	if p.Ledger.Reserved != 0 || p.Ledger.Spent != 10500000 {
		t.Fatalf("completion reserve was double billed: %+v", p.Ledger)
	}
}

func TestLeaderBudgetValidationBeforeSourceOrModel(t *testing.T) {
	provider := &batchProvider{}
	rt, agents := batchRuntime(t, provider)
	agents[1].Budget = domain.BudgetLimits{Soft: 100, Hard: 100}
	_, err := rt.Review(context.Background(), domain.SourceRequest{RunID: "invalid-leader"}, agents)
	if domain.CodeOf(err) != domain.ErrInvalidConfig || provider.calls != 0 {
		t.Fatalf("leader soft/hard relationship accepted: %v", err)
	}
}

func TestCompletionReservePreventsPaidCompaction(t *testing.T) {
	provider := &budgetProvider{}
	rt, state, p := budgetFixture(t, domain.BudgetLimits{Soft: 11000000, Hard: 12000000}, provider)
	p.Ledger.Spent = 10500000
	rt.config.CompactThreshold = 1
	p.Messages = append(p.Messages, domain.Message{Role: "assistant", Content: "Evidence collected"}, domain.Message{Role: "tool", Content: "Evidence saved"})
	if err := rt.step(context.Background(), &state, p); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || p.Phase != "finalization" {
		t.Fatalf("paid compact consumed finalization reserve: calls %d phase %s", provider.calls, p.Phase)
	}
}

func verifySoftStatus(t *testing.T, status BudgetStatus) {
	t.Helper()
	if !status.Enabled || status.Spent != 10000000 || status.Reserved != 0 || status.Phase != "finalization" || status.RemainingToSoft == nil || *status.RemainingToSoft != 0 {
		t.Fatalf("incorrect status %+v", status)
	}
	if status.NextActionEstimate <= 0 || status.CompletionReserve <= 0 || !status.EstimateApproximate || status.EstimateKnown {
		t.Fatalf("missing or fabricated budget estimates %+v", status)
	}
}

func verifyFinalizationTools(t *testing.T, tools []domain.ToolDefinition) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == "shell" || tool.Name == "metadata" {
			t.Fatalf("finalization advertised investigation tool %s", tool.Name)
		}
	}
}

func TestFinalizationGitDiffDisablesExternalPrograms(t *testing.T) {
	root := gitFixture(t)
	marker := filepath.Join(t.TempDir(), "external-invoked")
	script := filepath.Join(t.TempDir(), "external-diff")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf invoked > '"+marker+"'\n"), 0o700); err != nil { //nolint:gosec // This local external-diff regression fixture must be executable by its owner.
		t.Fatal(err)
	}
	command := exec.CommandContext(context.Background(), "git", "config", "diff.external", script) //nolint:gosec // Test-owned repository and executable fixture path.
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture config %s %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(root, "code.go"), []byte("package modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, state, p := requestFixture(t, &pricedProvider{})
	p.Workspace = root
	p.Phase = finalizationPhase
	outcome := rt.executeTool(context.Background(), &state, p, domain.ToolCall{ID: "diff", Name: "git", Arguments: json.RawMessage(`{"args":["diff"]}`)})
	if outcome.Result.IsError || !strings.Contains(outcome.Result.Content, "package modified") {
		t.Fatalf("safe diff unavailable: %+v", outcome.Result)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finalization launched external diff: %v", err)
	}
}

func verifyBudgetWarnings(t *testing.T, noticeTypes []string, events []domain.Event, phase string) {
	t.Helper()
	callbacks := 0
	for _, kind := range noticeTypes {
		if kind == "budget_threshold" {
			callbacks++
		}
	}
	thresholds := 0
	for _, event := range events {
		if event.Type == "budget_threshold" {
			thresholds++
		}
	}
	if callbacks != 2 || thresholds != 2 || phase != finalizationPhase {
		t.Fatalf("warnings or phase lost/duplicated: callbacks %d events %d phase %s", callbacks, thresholds, phase)
	}
}
