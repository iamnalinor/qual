package app

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/iamnalinor/qual/internal/domain"
)

type budgetView struct {
	Spent            domain.Money `json:"spent"`
	Soft             domain.Money `json:"soft"`
	Hard             domain.Money `json:"hard"`
	Reserved         domain.Money `json:"reserved"`
	CostKnown        bool         `json:"cost_known"`
	ThresholdPercent int          `json:"threshold_percent,omitempty"`
}
type roleView struct {
	Model  string      `json:"model,omitempty"`
	Role   domain.Role `json:"role,omitempty"`
	Phase  string      `json:"phase,omitempty"`
	Budget *budgetView `json:"budget,omitempty"`
}
type visibleEvent struct {
	SchemaVersion int            `json:"schema_version"`
	Sequence      int64          `json:"sequence,omitempty"`
	Time          time.Time      `json:"time"`
	RunID         domain.RunID   `json:"run_id"`
	AgentID       domain.AgentID `json:"agent_id,omitempty"`
	Type          string         `json:"type"`
	roleView
	Action      string            `json:"action,omitempty"`
	Target      string            `json:"target,omitempty"`
	Tool        string            `json:"tool,omitempty"`
	CallID      string            `json:"call_id,omitempty"`
	IsError     bool              `json:"is_error,omitempty"`
	ErrorCode   domain.ErrorCode  `json:"error_code,omitempty"`
	ResultBytes int               `json:"result_bytes,omitempty"`
	ArtifactID  string            `json:"artifact_id,omitempty"`
	Artifacts   []domain.Artifact `json:"artifacts,omitempty"`
}

func visibleHistory(events []domain.Event) ([]visibleEvent, error) {
	history := []visibleEvent{}
	roles := map[domain.AgentID]roleView{}
	for _, event := range events {
		view, ok, err := projectEvent(event, roles)
		if err != nil {
			return nil, err
		}
		if ok {
			history = append(history, view)
		}
	}
	return history, nil
}

func projectEvent(event domain.Event, roles map[domain.AgentID]roleView) (visibleEvent, bool, error) {
	view := visibleEvent{SchemaVersion: 1, Sequence: event.Sequence, Time: event.Time, RunID: event.RunID, AgentID: event.AgentID, Type: event.Type, roleView: roles[event.AgentID]}
	var err error
	switch event.Type {
	case "agent_checkpoint":
		err = checkpointView(event.Data, &view)
		view.Type = "agent_progress"
	case "tool_intent":
		var call domain.ToolCall
		err = json.Unmarshal(event.Data, &call)
		view.Tool = safeIdentifier(call.Name)
		view.Target = safeToolTarget(call)
		view.CallID = safeIdentifier(call.ID)
		view.Action = "execute_tool"
	case "tool_result":
		err = toolResultView(event.Data, &view)
	case "budget_threshold", "budget_phase":
		err = budgetEventView(event.Data, &view)
	case "application_artifact":
		var artifact struct {
			ID string `json:"artifact_id"`
		}
		err = json.Unmarshal(event.Data, &artifact)
		view.ArtifactID = artifact.ID
		view.Action = "save_artifact"
	case "mcp_audit":
		var audit struct{ Tool, CallID, Phase, Outcome string }
		err = json.Unmarshal(event.Data, &audit)
		view.Tool = safeIdentifier(audit.Tool)
		view.CallID = safeIdentifier(audit.CallID)
		view.Action = "mcp_" + safeIdentifier(audit.Phase)
		view.IsError = audit.Outcome == "error"
	default:
		return view, false, nil
	}
	roles[event.AgentID] = view.roleView
	return view, true, err
}

func checkpointView(data []byte, view *visibleEvent) error {
	var checkpoint struct {
		Spec struct {
			Model string
			Role  domain.Role
		}
		Phase  string
		Ledger struct {
			Spent, Reserved domain.Money
			CostKnown       bool `json:"cost_known"`
			Limits          domain.BudgetLimits
		}
	}
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return err
	}
	view.Model = checkpoint.Spec.Model
	view.Role = checkpoint.Spec.Role
	view.Phase = checkpoint.Phase
	ledger := checkpoint.Ledger
	view.Budget = &budgetView{Spent: ledger.Spent, Soft: ledger.Limits.Soft, Hard: ledger.Limits.Hard, Reserved: ledger.Reserved, CostKnown: ledger.CostKnown}
	view.Action = "role_progress"
	return nil
}

func toolResultView(data []byte, view *visibleEvent) error {
	var outcome struct {
		Call     domain.ToolCall
		Result   domain.ToolResult
		Finished *struct{ Artifacts []domain.Artifact }
	}
	if err := json.Unmarshal(data, &outcome); err != nil {
		return err
	}
	view.Tool = safeIdentifier(outcome.Call.Name)
	view.Target = safeToolTarget(outcome.Call)
	view.CallID = safeIdentifier(outcome.Call.ID)
	view.IsError = outcome.Result.IsError
	view.ResultBytes = len(outcome.Result.Content)
	view.ArtifactID = outcome.Result.ArtifactID
	view.Action = "tool_completed"
	if view.IsError {
		view.Action = "tool_failed"
		view.ErrorCode = publicToolErrorCode(outcome.Result.Content)
	}
	if outcome.Finished != nil {
		view.Artifacts = outcome.Finished.Artifacts
	}
	return nil
}

func budgetEventView(data []byte, view *visibleEvent) error {
	var notice struct {
		Threshold int `json:"threshold_percent"`
		Status    struct {
			Spent, Reserved domain.Money
			CostKnown       bool `json:"cost_known"`
			Limits          domain.BudgetLimits
			Phase           string
		}
	}
	if err := json.Unmarshal(data, &notice); err != nil {
		return err
	}
	state := notice.Status
	view.Phase = state.Phase
	view.Budget = &budgetView{Spent: state.Spent, Soft: state.Limits.Soft, Hard: state.Limits.Hard, Reserved: state.Reserved, CostKnown: state.CostKnown, ThresholdPercent: notice.Threshold}
	view.Action = "budget_notice"
	return nil
}

func safeIdentifier(value string) string {
	if len(value) > 128 {
		return "external"
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_-.:/", r) {
			return "external"
		}
	}
	return value
}

func (a *App) renderProgress(view visibleEvent) error {
	if a.terminal != nil {
		a.terminalActivity(view)
		return nil
	}
	action := a.text(actionTitle(view.Action))
	if view.Tool != "" {
		action += " " + view.Tool
	}
	message := a.text("Action") + ": " + action
	if a.JSON {
		return json.NewEncoder(a.Err).Encode(struct {
			visibleEvent
			Message string `json:"message"`
		}{view, message})
	}
	_, err := fmt.Fprintf(a.Err, "%s %s [%s %s] %s", view.RunID, view.AgentID, view.Model, a.text(view.Phase), message)
	if err != nil {
		return err
	}
	return a.progressBudget(view.Budget)
}

func (a *App) progressBudget(budget *budgetView) error {
	if budget == nil {
		_, err := fmt.Fprintln(a.Err)
		return err
	}
	_, err := fmt.Fprintf(a.Err, "; %s: spent=%d soft=%d hard=%d reserved=%d cost_known=%t\n", a.text("Budget"), budget.Spent, budget.Soft, budget.Hard, budget.Reserved, budget.CostKnown)
	return err
}

func (s *observedStore) progress(event domain.Event) error {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.progressUnlocked(event)
}

func (s *observedStore) progressUnlocked(event domain.Event) error {
	if s.roles == nil {
		s.roles = map[domain.AgentID]roleView{}
	}
	view, ok, err := projectEvent(event, s.roles)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return s.app.renderProgress(view)
}

func (s *observedStore) budgetEvent(event domain.Event) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	if s.progressErr == nil {
		s.progressErr = s.progressUnlocked(event)
	}
}

func actionTitle(action string) string {
	switch action {
	case "execute_tool":
		return "Execute tool"
	case "tool_completed":
		return "Tool completed"
	case "tool_failed":
		return "Tool failed"
	case "budget_notice":
		return "Budget update"
	case "role_progress":
		return "Role progress"
	case "save_artifact":
		return "Saving artifact"
	default:
		return action
	}
}

func publicToolErrorCode(content string) domain.ErrorCode {
	for _, code := range []domain.ErrorCode{domain.ErrInvalidConfig, domain.ErrAuth, domain.ErrUnsupportedCapability, domain.ErrCancelled, domain.ErrBudget, domain.ErrIncomplete, domain.ErrTool} {
		if strings.Contains(content, string(code)+":") {
			return code
		}
	}
	return domain.ErrTool
}

func safeToolTarget(call domain.ToolCall) string {
	if call.Name != "read_file" && call.Name != "write_file" {
		return ""
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ""
	}
	target := args.Path
	if len(target) > 160 || path.IsAbs(target) || path.Clean(target) != target || target == ".." || strings.HasPrefix(target, "../") {
		return ""
	}
	if strings.IndexFunc(target, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return ""
	}
	return target
}
