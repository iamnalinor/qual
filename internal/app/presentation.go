package app

import (
	"fmt"
	"strings"
	"text/tabwriter"
)

type tableRow []string

func (a *App) heading(title string) error {
	_, err := fmt.Fprintf(a.Out, "%s\n\n", a.text(title))
	return err
}

func (a *App) table(headers tableRow, rows []tableRow) error {
	writer := tabwriter.NewWriter(a.Out, 0, 0, 3, ' ', 0)
	if len(headers) > 0 {
		if _, err := fmt.Fprintln(writer, strings.Join(headers, "\t")); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(writer, strings.Join(cleanCells(row), "\t")); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (a *App) line(format string, args ...any) error {
	_, err := fmt.Fprintf(a.Out, format, args...)
	return err
}

func (a *App) renderDoctor(report doctorReport) error {
	if a.JSON {
		return a.Output(report)
	}
	if err := a.heading("Qual doctor"); err != nil {
		return err
	}
	activation := "not active (direct invocation)"
	if report.Sandbox.ReportedActive {
		activation = "launcher-reported active"
	}
	if err := a.table(nil, []tableRow{{a.text("System"), report.OS + " / " + report.Arch}, {a.text("State directory"), report.Home}, {a.text("Sandbox activation"), a.text(activation)}}); err != nil {
		return err
	}
	rows := make([]tableRow, 0, len(report.Checks))
	for _, check := range report.Checks {
		rows = append(rows, tableRow{a.text(check.Name), a.text(check.Status), check.Path})
	}
	if err := a.line("\n"); err != nil {
		return err
	}
	if err := a.table(tableRow{a.text("CHECK"), a.text("STATUS"), a.text("LOCATION")}, rows); err != nil {
		return err
	}
	return a.doctorNextSteps(report)
}

func (a *App) doctorNextSteps(report doctorReport) error {
	actions := []string{}
	for _, check := range report.Checks {
		if check.Action != "" {
			actions = append(actions, a.text(check.Action))
		}
	}
	if !report.ConnectionsChecked {
		actions = append(actions, a.text("Provider connections were not checked. Use qual doctor --connections for unpaid authentication probes."))
	}
	if len(actions) == 0 {
		return a.line("\n%s\n", a.text("All requested checks passed."))
	}
	if err := a.line("\n%s\n", a.text("Next steps")); err != nil {
		return err
	}
	for _, action := range actions {
		if err := a.line("  • %s\n", action); err != nil {
			return err
		}
	}
	return nil
}

func cleanCells(row tableRow) tableRow {
	result := make(tableRow, len(row))
	for i, value := range row {
		result[i] = strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return ' '
			}
			return r
		}, value)
	}
	return result
}
