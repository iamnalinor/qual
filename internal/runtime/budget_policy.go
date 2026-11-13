package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/iamnalinor/qual/internal/budget"
	"github.com/iamnalinor/qual/internal/domain"
)

const (
	investigationPhase   = "investigation"
	finalizationPhase    = "finalization"
	finalizationGuidance = "Budget finalization phase: stop new investigation. Use existing findings and saved evidence. Batch final write_file calls for all required outputs and finish in one response. Read files/artifacts or inspect git diff if needed. Shell and extension investigation tools are unavailable. Limits cannot be changed by the model."
)

// BudgetStatus describes actual accounting and the separately protected completion margin.
// CompletionReserve is part of remaining hard capacity, not a second ledger charge.
type BudgetStatus struct {
	budget.State
	Enabled             bool          `json:"enabled"`
	RemainingToSoft     *domain.Money `json:"remaining_to_soft"`
	Phase               string        `json:"phase"`
	NextAction          string        `json:"next_action"`
	NextActionEstimate  domain.Money  `json:"next_action_estimate"`
	EstimateKnown       bool          `json:"estimate_known"`
	EstimateApproximate bool          `json:"estimate_approximate"`
	CompletionReserve   domain.Money  `json:"completion_reserve"`
}
type budgetNotice struct {
	ThresholdPercent int          `json:"threshold_percent,omitempty"`
	Status           BudgetStatus `json:"status"`
}

func (r *Runtime) agentRequest(state *domain.RunState, p *agentProgress) domain.ModelRequest {
	return domain.ModelRequest{RunID: state.RunID, AgentID: p.Spec.ID, Model: p.Spec.Model, ReasoningEffort: p.Spec.ReasoningEffort, Messages: p.Messages, Tools: r.roleDefinitions(p), MaxOutputTokens: outputLimit(p.Catalog), ProviderState: p.ProviderState}
}

func (r *Runtime) roleDefinitions(p *agentProgress) []domain.ToolDefinition {
	definitions := r.definitions()
	if p.Phase != finalizationPhase {
		return definitions
	}
	result := make([]domain.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if finalizationName(definition.Name) {
			result = append(result, definition)
		}
	}
	return result
}

func finalizationName(name string) bool {
	switch name {
	case "read_file", "write_file", "list_files", "search", "git", "budget_status", "read_artifact", "finish":
		return true
	default:
		return false
	}
}

func (r *Runtime) budgetStatus(state *domain.RunState, p *agentProgress) (BudgetStatus, error) {
	estimate, known, err := r.estimateDetails(p.Catalog, r.agentRequest(state, p))
	if err != nil {
		return BudgetStatus{}, err
	}
	status := BudgetStatus{State: p.Ledger, Enabled: p.Spec.Budget.Soft > 0, Phase: p.Phase, NextAction: "model_turn", NextActionEstimate: estimate, EstimateKnown: known, EstimateApproximate: !known, CompletionReserve: p.CompletionReserve}
	if status.Phase == "" {
		status.Phase = investigationPhase
	}
	if p.Phase == finalizationPhase {
		status.NextAction = "finish_submission"
	}
	if status.Enabled {
		remaining := p.Spec.Budget.Soft - p.Ledger.Spent - p.Ledger.Reserved
		if remaining < 0 {
			remaining = 0
		}
		status.RemainingToSoft = &remaining
	}
	return status, nil
}

func (r *Runtime) prepareBudget(ctx context.Context, state *domain.RunState, p *agentProgress, request *domain.ModelRequest) error {
	if p.Spec.Budget.Soft <= 0 {
		return nil
	}
	if err := r.afterBudget(ctx, state, p); err != nil {
		return err
	}
	completion := r.agentRequest(state, p)
	completion.Tools = finalizationDefinitions(r.definitions())
	completion.Messages = append(append([]domain.Message(nil), p.Messages...), domain.Message{Role: "user", Content: finalizationGuidance})
	reserve, err := r.estimate(p, completion)
	if err != nil {
		return err
	}
	p.CompletionReserve = reserve
	estimate, err := r.estimate(p, *request)
	if err != nil {
		return err
	}
	if investigationUsesReserve(p, estimate, reserve) {
		if err = r.setFinalization(ctx, state, p); err != nil {
			return err
		}
	}
	if p.Phase == finalizationPhase {
		if len(request.Tools) == 0 {
			return coded(domain.ErrBudget, "paid compaction would consume the completion reserve", nil)
		}
		*request = r.agentRequest(state, p)
		p.CompletionReserve, err = r.estimate(p, *request)
	}
	return err
}

func finalizationDefinitions(definitions []domain.ToolDefinition) []domain.ToolDefinition {
	result := make([]domain.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if finalizationName(definition.Name) {
			result = append(result, definition)
		}
	}
	return result
}

func investigationUsesReserve(p *agentProgress, estimate, reserve domain.Money) bool {
	if p.Phase == finalizationPhase || p.Spec.Budget.Hard <= 0 {
		return false
	}
	remaining := p.Spec.Budget.Hard - p.Ledger.Spent - p.Ledger.Reserved
	return remaining < reserve || estimate > remaining-reserve
}

func (r *Runtime) afterBudget(ctx context.Context, state *domain.RunState, p *agentProgress) error {
	if p.Spec.Budget.Soft <= 0 {
		return nil
	}
	if err := r.budgetWarnings(ctx, state, p); err != nil {
		return err
	}
	if p.Ledger.Spent+p.Ledger.Reserved >= p.Spec.Budget.Soft {
		return r.setFinalization(ctx, state, p)
	}
	return nil
}

func (r *Runtime) budgetWarnings(ctx context.Context, state *domain.RunState, p *agentProgress) error {
	if p.BudgetWarnings == nil {
		p.BudgetWarnings = map[int]bool{}
	}
	for _, percent := range []int{80, 100} {
		threshold := p.Spec.Budget.Soft
		if percent == 80 {
			threshold -= threshold / 5
		}
		if p.BudgetWarnings[percent] || p.Ledger.Spent+p.Ledger.Reserved < threshold {
			continue
		}
		status, err := r.budgetStatus(state, p)
		if err != nil {
			return err
		}
		notice := budgetNotice{ThresholdPercent: percent, Status: status}
		if err = r.event(ctx, state.RunID, p.Spec.ID, "budget_threshold", notice); err != nil {
			return err
		}
		applyBudgetNotice(p, "budget_threshold", notice)
	}
	return nil
}

func (r *Runtime) setFinalization(ctx context.Context, state *domain.RunState, p *agentProgress) error {
	if p.Phase == finalizationPhase {
		return nil
	}
	p.Phase = finalizationPhase
	request := r.agentRequest(state, p)
	request.Messages = append(append([]domain.Message(nil), p.Messages...), domain.Message{Role: "user", Content: finalizationGuidance})
	reserve, err := r.estimate(p, request)
	if err != nil {
		return err
	}
	p.CompletionReserve = reserve
	status, err := r.budgetStatus(state, p)
	if err != nil {
		return err
	}
	notice := budgetNotice{Status: status}
	if err = r.event(ctx, state.RunID, p.Spec.ID, "budget_phase", notice); err != nil {
		return err
	}
	applyBudgetNotice(p, "budget_phase", notice)
	return nil
}

func applyBudgetNotice(p *agentProgress, kind string, notice budgetNotice) {
	if kind == "budget_phase" {
		p.Phase = notice.Status.Phase
		p.CompletionReserve = notice.Status.CompletionReserve
		p.Messages = append(p.Messages, domain.Message{Role: "user", Content: finalizationGuidance})
		return
	}
	if p.BudgetWarnings == nil {
		p.BudgetWarnings = map[int]bool{}
	}
	p.BudgetWarnings[notice.ThresholdPercent] = true
	data, err := json.Marshal(notice)
	if err != nil {
		panic(fmt.Sprintf("invalid internal budget notice: %v", err))
	}
	p.Messages = append(p.Messages, domain.Message{Role: "user", Content: "Runtime budget threshold warning: " + string(data)})
}

func budgetGuidance(limits domain.BudgetLimits) string {
	if limits.Soft <= 0 {
		return ""
	}
	return fmt.Sprintf(" Budget mode is enabled. Soft target USD %s; hard USD %s (0 means unset). Check budget_status when choosing an investigation plan or responding to a runtime budget warning; reuse the latest status between related tool reads. Runtime warns at 80%% and 100%% of soft and protects one estimated completion call below hard. Adjust investigation to remaining_to_soft; when instructed to finalize, batch output writes and finish. Only runtime controls limits.", budget.Format(limits.Soft), budget.Format(limits.Hard))
}
