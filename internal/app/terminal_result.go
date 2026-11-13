package app

import (
	"fmt"
	"path/filepath"

	"github.com/iamnalinor/qual/internal/domain"
)

func (a *App) terminalResult(state domain.RunState, reportPath, output string) error {
	title := a.text("Review") + " · " + a.text(string(state.Status))
	for _, result := range state.Results {
		if result.Role == domain.RoleSetup {
			title = a.text("Project setup") + " · " + a.text(string(state.Status))
		}
	}
	if err := a.heading(title); err != nil {
		return err
	}
	if state.Status == domain.StatusNoChanges {
		if err := a.line("%s\n", a.text("No changes in the selected scope. Use qual review --worktree for tracked working files, or --include-untracked to include new files.")); err != nil {
			return err
		}
	}
	if err := a.resultFindings(state); err != nil {
		return err
	}
	if err := a.resultUsage(state); err != nil {
		return err
	}
	return a.resultActions(state, reportPath, output)
}

func (a *App) resultActions(state domain.RunState, reportPath, output string) error {
	if output != "" {
		reportPath = filepath.Join(output, "report.md")
	}
	if err := a.line("\n%s: %s\n  qual sessions show %s\n", a.text("Report"), reportPath, state.RunID); err != nil {
		return err
	}
	if state.Status != domain.StatusCompleted && state.Status != domain.StatusNoChanges {
		return a.line("  qual sessions resume %s\n", state.RunID)
	}
	return nil
}

func (a *App) resultFindings(state domain.RunState) error {
	for _, result := range state.Results {
		if result.Role != domain.RoleLeader || result.Status != domain.StatusCompleted {
			continue
		}
		if err := a.line("%d %s\n", len(result.Findings), a.text("leader-confirmed findings")); err != nil {
			return err
		}
		for _, finding := range result.Findings {
			if err := a.line("\n  %s · %s:%d\n  %s\n", finding.Severity, finding.Anchor.Path, finding.Anchor.StartLine, finding.Title); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) resultUsage(state domain.RunState) error {
	var cost domain.Money
	known := len(state.Results) > 0
	for _, result := range state.Results {
		cost += result.Usage.Cost
		known = known && result.Usage.CostKnown
	}
	label := a.text("Cost unknown · see your provider account for billing")
	if known {
		label = fmt.Sprintf("%s %s", moneyUSD(cost), a.text("total spent"))
	}
	return a.line("%s\n", label)
}
