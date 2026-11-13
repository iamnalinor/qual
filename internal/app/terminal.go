package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/tui"
)

func (a *App) interactive() bool {
	return !a.NonInteractive && !a.JSON && tui.Enabled(a.In, a.Err) && tui.Enabled(a.In, a.Out)
}

func (a *App) withTerminal(ctx context.Context, title string, work func(context.Context) error) error {
	if a.terminal != nil || !a.interactive() {
		return work(ctx)
	}
	return tui.Run(ctx, a.In, a.Err, title, a.UILanguage, func(ctx context.Context, ui *tui.UI) error {
		a.terminal = ui
		defer func() { a.terminal = nil }()
		return work(ctx)
	})
}

func (a *App) choose(ctx context.Context, title, description string, options []tui.Option) (string, error) {
	for i := range options {
		options[i].Label = a.text(options[i].Label)
		options[i].Description = a.text(options[i].Description)
	}
	return a.terminal.Ask(ctx, tui.Prompt{Title: a.text(title), Description: a.text(description), Options: options})
}

// Welcome presents the next useful action for a new or returning user.
func (a *App) Welcome(ctx context.Context) error {
	e, err := a.Config(ctx, "")
	if err != nil {
		return err
	}
	if err = a.selectUI(e); err != nil {
		return err
	}
	if a.JSON {
		return a.Output(map[string]any{"configured": len(e.Agents) > 0, "next": "qual init"})
	}
	return a.welcomeText()
}

func (a *App) welcomeText() error {
	if err := a.heading("Qual · code review with a second opinion"); err != nil {
		return err
	}
	return a.line("%s\n\n  qual init           %s\n  qual review         %s\n  qual sessions list  %s\n  qual --help         %s\n", a.text("A reviewer investigates your changes. A leader checks the findings."), a.text("Connect a provider and choose models"), a.text("Review changes in this directory"), a.text("Find reports or resume a session"), a.text("All commands and flags"))
}

func terminalBudget(view *budgetView) string {
	if view == nil {
		return "Cost unknown"
	}
	cost := "Cost unknown"
	if view.CostKnown {
		cost = moneyUSD(view.Spent) + " spent"
	}
	limit := "Unlimited"
	if view.Hard > 0 {
		limit = moneyUSD(view.Hard) + " limit"
	}
	return fmt.Sprintf("%s · %s reserved · %s", cost, moneyUSD(view.Reserved), limit)
}

func (a *App) terminalActivity(view visibleEvent) {
	role := string(view.AgentID)
	if role == "" {
		role = a.text("Session")
	}
	action := a.text(actionTitle(view.Action))
	if view.Tool != "" {
		action += " · " + view.Tool
		if view.Target != "" {
			action += " " + view.Target
		}
	}
	detail := strings.TrimSpace(fmt.Sprintf("%s · %s · %d bytes", view.Model, a.text(view.Phase), view.ResultBytes))
	budgetLabel := ""
	if view.Budget != nil {
		budgetLabel = role + " · " + a.localizedBudget(view.Budget)
	}
	event := tui.Activity{Summary: role + " · " + action, Detail: detail, Budget: budgetLabel}
	if view.Type == "agent_progress" {
		event.Transient = true
		event.Summary = role + " · " + a.text(view.Phase)
	}
	if view.CallID != "" {
		event.Key = role + "/" + view.CallID
	}
	a.terminal.Activity(event)
}

func (a *App) sessionHeader(snapshot domain.Snapshot, agents []domain.AgentSpec) {
	if a.terminal == nil {
		return
	}
	var models []string
	for _, agent := range agents {
		models = append(models, string(agent.Role)+": "+agent.Model)
	}
	info := executionEnvironment()
	sandbox := a.text("Direct run · OS isolation not verified")
	if info.ExternalSandbox == "launcher-reported active" {
		sandbox = a.text("OS sandbox active · reported by launcher")
	}
	if info.Network != "" {
		sandbox += " · " + info.Network
	}
	a.terminal.Header(fmt.Sprintf("%s · %d %s\n%s · %s → %s\n%s\n%s", snapshot.Repository, len(snapshot.Files), a.text("changed files"), shortRevision(string(snapshot.RunID)), shortRevision(snapshot.BaseCommit), shortRevision(snapshot.HeadCommit), strings.Join(models, " · "), sandbox))
}

func shortRevision(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func moneyUSD(value domain.Money) string {
	if value == 0 {
		return "$0"
	}
	if value > 0 && value < 100000 {
		return "<$0.0001"
	}
	return fmt.Sprintf("$%.4f", float64(value)/1e9)
}

func (a *App) localizedBudget(view *budgetView) string {
	label := terminalBudget(view)
	for _, word := range []string{"Cost unknown", "spent", "reserved", "limit", "Unlimited"} {
		label = strings.ReplaceAll(label, word, a.text(word))
	}
	return label
}
