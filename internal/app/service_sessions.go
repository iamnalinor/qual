package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

func (a *App) renderSessions(states []domain.RunState) error {
	if a.JSON {
		return a.Output(states)
	}
	if err := a.heading("Sessions"); err != nil {
		return err
	}
	if len(states) == 0 {
		return a.line("%s\n", a.text("No sessions yet. Run qual review to begin."))
	}
	rows := make([]tableRow, 0, len(states))
	for _, state := range states {
		models := make([]string, 0, len(state.Agents))
		for _, agent := range state.Agents {
			models = append(models, string(agent.ID)+": "+agent.Model)
		}
		rows = append(rows, tableRow{string(state.RunID), string(state.Status), state.Snapshot.Repository, strings.Join(models, ", "), sessionTime(state)})
	}
	return a.table(tableRow{a.text("SESSION"), a.text("STATUS"), a.text("PROJECT"), a.text("ROLES / MODELS"), a.text("UPDATED")}, rows)
}

func sessionTime(state domain.RunState) string {
	stamp := state.UpdatedAt
	if stamp.IsZero() {
		stamp = state.CreatedAt
	}
	return stamp.Format(time.RFC3339)
}

func (a *App) renderSession(state domain.RunState, history []visibleEvent) error {
	if a.JSON {
		return a.Output(struct {
			domain.RunState
			History []visibleEvent `json:"history"`
		}{state, history})
	}
	if err := a.heading("Session"); err != nil {
		return err
	}
	if err := a.table(nil, []tableRow{{a.text("SESSION"), string(state.RunID)}, {a.text("STATUS"), string(state.Status)}, {a.text("PROJECT"), string(state.Snapshot.ProjectID)}, {a.text("Snapshot"), state.Snapshot.BaseCommit + " → " + state.Snapshot.HeadCommit}, {a.text("Updated"), sessionTime(state)}}); err != nil {
		return err
	}
	if err := a.line("\n%s\n", a.text("Execution history")); err != nil {
		return err
	}
	rows := make([]tableRow, 0, len(history))
	for _, event := range history {
		detail := event.Tool
		if event.ArtifactID != "" {
			detail += " artifact=" + event.ArtifactID
		}
		if event.Budget != nil {
			detail += fmt.Sprintf(" spent=%d soft=%d", event.Budget.Spent, event.Budget.Soft)
		}
		rows = append(rows, tableRow{string(event.AgentID), event.Model, event.Phase, a.text(actionTitle(event.Action)), detail})
	}
	if err := a.table(tableRow{a.text("ROLE"), a.text("MODEL"), a.text("PHASE"), a.text("ACTION"), a.text("DETAILS")}, rows); err != nil {
		return err
	}
	for _, result := range state.Results {
		if err := a.line("\n%s (%s)\n%s\n", result.AgentID, result.Status, result.Report); err != nil {
			return err
		}
	}
	return nil
}
