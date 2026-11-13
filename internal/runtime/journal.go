package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iamnalinor/qual/internal/budget"
	"github.com/iamnalinor/qual/internal/domain"
)

type agentProgress struct {
	Phase                  string                     `json:"phase"`
	CompletionReserve      domain.Money               `json:"completion_reserve"`
	BudgetWarnings         map[int]bool               `json:"budget_warnings"`
	Response               *savedResponse             `json:"response,omitempty"`
	Catalog                domain.ModelInfo           `json:"catalog"`
	ApproximateReservation bool                       `json:"approximate_reservation"`
	Spec                   domain.AgentSpec           `json:"spec"`
	Workspace              string                     `json:"workspace"`
	Messages               []domain.Message           `json:"messages"`
	ProviderState          json.RawMessage            `json:"provider_state"`
	Ledger                 budget.State               `json:"ledger"`
	Turns                  int                        `json:"turns"`
	Finished               *domain.AgentResult        `json:"finished,omitempty"`
	Pending                []domain.ToolCall          `json:"pending,omitempty"`
	Usage                  domain.Usage               `json:"usage"`
	Outcomes               map[string]toolOutcome     `json:"outcomes"`
	Intents                map[string]domain.ToolCall `json:"intents"`
}
type toolOutcome struct {
	Call     domain.ToolCall     `json:"call"`
	Result   domain.ToolResult   `json:"result"`
	Finished *domain.AgentResult `json:"finished,omitempty"`
}

func (r *Runtime) event(ctx context.Context, id domain.RunID, agent domain.AgentID, kind string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode runtime event: %w", err)
	}
	event := domain.Event{SchemaVersion: 1, RunID: id, AgentID: agent, Type: kind, Time: time.Now().UTC(), Data: data}
	if err = r.config.Store.Append(ctx, event); err != nil {
		return err
	}
	if r.config.OnEvent != nil && (kind == "budget_threshold" || kind == "budget_phase") {
		r.config.OnEvent(event)
	}
	return nil
}

func (r *Runtime) checkpoint(ctx context.Context, id domain.RunID, p *agentProgress) error {
	return r.event(ctx, id, p.Spec.ID, "agent_checkpoint", p)
}

func (r *Runtime) recover(ctx context.Context, id domain.RunID) (map[domain.AgentID]*agentProgress, error) {
	events, err := r.config.Store.Events(ctx, id)
	if err != nil {
		return nil, err
	}
	agents := map[domain.AgentID]*agentProgress{}
	for _, event := range events {
		if err = restoreEvent(agents, event); err != nil {
			return nil, err
		}
	}
	for _, p := range agents {
		normalizeOpaque(p)
		if p.Finished != nil && p.Finished.Status == domain.StatusCancelled {
			p.Finished = nil
		}
		if len(p.Ledger.Reservations) > 0 {
			p.Usage.CostKnown = false
			p.Ledger.CostKnown = false
			p.Ledger.Unknown = len(p.Ledger.Reservations)
		}
	}
	return agents, nil
}

func restoreEvent(agents map[domain.AgentID]*agentProgress, e domain.Event) error {
	switch e.Type {
	case "budget_threshold", "budget_phase":
		return restoreBudgetEvent(agents, e)
	case "agent_checkpoint":
		var p agentProgress
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return fmt.Errorf("decode agent checkpoint: %w", err)
		}
		agents[e.AgentID] = &p
	case "tool_intent":
		var call domain.ToolCall
		if err := json.Unmarshal(e.Data, &call); err != nil {
			return fmt.Errorf("decode intent: %w", err)
		}
		p := agents[e.AgentID]
		if p == nil {
			return coded(domain.ErrIncomplete, "intent lacks agent checkpoint", nil)
		}
		p.Intents[call.ID] = call
	case "tool_result":
		var outcome toolOutcome
		if err := json.Unmarshal(e.Data, &outcome); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
		p := agents[e.AgentID]
		if p == nil {
			return coded(domain.ErrIncomplete, "result lacks agent checkpoint", nil)
		}
		p.Outcomes[outcome.Call.ID] = outcome
		if outcome.Finished != nil {
			p.Finished = outcome.Finished
		}
	default:
	}
	return nil
}

type savedResponse struct {
	Text    string            `json:"text"`
	Calls   []domain.ToolCall `json:"calls"`
	Summary bool              `json:"summary"`
}

func restoreBudgetEvent(agents map[domain.AgentID]*agentProgress, e domain.Event) error {
	var notice budgetNotice
	if err := json.Unmarshal(e.Data, &notice); err != nil {
		return fmt.Errorf("decode budget notice: %w", err)
	}
	p := agents[e.AgentID]
	if p == nil {
		return coded(domain.ErrIncomplete, "budget notice lacks agent checkpoint", nil)
	}
	applyBudgetNotice(p, e.Type, notice)
	return nil
}

func normalizeOpaque(p *agentProgress) {
	if string(p.ProviderState) == "null" {
		p.ProviderState = nil
	}
	for i := range p.Messages {
		if string(p.Messages[i].ProviderData) == "null" {
			p.Messages[i].ProviderData = nil
		}
	}
}
