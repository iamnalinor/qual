package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iamnalinor/qual/internal/tui"

	"github.com/iamnalinor/qual/internal/config"

	"github.com/iamnalinor/qual/internal/auth"
	"github.com/iamnalinor/qual/internal/mcp"
)

// FilePaths reports generated artifacts without changing their machine-readable contract.
func (a *App) FilePaths(paths map[string]string) error {
	if a.JSON {
		return a.Output(paths)
	}
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if err := a.heading("Saved files"); err != nil {
		return err
	}
	rows := make([]tableRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, tableRow{a.text(key), paths[key]})
	}
	return a.table(nil, rows)
}

func (a *App) confirmation(title string, value any, data any) error {
	if a.terminal != nil {
		a.terminal.Activity(tui.Activity{Summary: a.text(title) + ": " + fmt.Sprint(value)})
		return nil
	}
	if a.JSON {
		return a.Output(data)
	}
	return a.line("%s: %v\n", a.text(title), value)
}

func (a *App) renderProfiles(profiles []auth.Profile) error {
	if a.JSON {
		return a.Output(profiles)
	}
	if err := a.heading("Credential profiles"); err != nil {
		return err
	}
	if len(profiles) == 0 {
		return a.line("%s\n", a.text("No accounts saved. Run qual init to connect a provider and choose models."))
	}
	rows := make([]tableRow, 0, len(profiles))
	for _, profile := range profiles {
		account := profile.Email
		if account == "" {
			account = profile.Subject
		}
		expiry := "—"
		if !profile.ExpiresAt.IsZero() {
			expiry = profile.ExpiresAt.Format(time.RFC3339)
		}
		rows = append(rows, tableRow{profile.ID, profile.Kind, account, expiry})
	}
	return a.table(tableRow{a.text("PROFILE"), a.text("PROVIDER"), a.text("ACCOUNT"), a.text("EXPIRES")}, rows)
}

func (a *App) renderMCP(infos []mcp.ServerInfo) error {
	if a.JSON {
		return a.Output(infos)
	}
	if err := a.heading("MCP connections"); err != nil {
		return err
	}
	if len(infos) == 0 {
		return a.line("%s\n", a.text("No MCP servers selected. Configure mcp_servers and mcp to enable discovery."))
	}
	rows := make([]tableRow, 0, len(infos))
	for _, info := range infos {
		names := make([]string, 0, len(info.Tools))
		for _, tool := range info.Tools {
			names = append(names, tool.Name)
		}
		rows = append(rows, tableRow{info.ID, a.text("ready"), strings.Join(names, ", ")})
	}
	return a.table(tableRow{a.text("SERVER"), a.text("STATUS"), a.text("AUTHORIZED TOOLS")}, rows)
}

func (a *App) renderCleanup(paths []string) error {
	if a.JSON {
		return a.Output(map[string]any{"removed": paths})
	}
	if err := a.line("%s: %d\n", a.text("Removed disposable worktrees"), len(paths)); err != nil {
		return err
	}
	for _, path := range paths {
		if err := a.line("  %s\n", path); err != nil {
			return err
		}
	}
	return a.line("%s\n", a.text("Session evidence, settings, active and pinned worktrees are retained."))
}

func (a *App) renderInit(accepted bool, settings config.Settings) error {
	if a.JSON {
		return a.Output(map[string]any{"accepted": accepted, "settings": settings})
	}
	title := "Settings preview only"
	if accepted {
		title = "Settings activated"
	}
	if err := a.line("%s: %s\n", a.text(title), filepath.Join(a.Home, "settings.yaml")); err != nil {
		return err
	}
	if accepted {
		return a.line("\n%s\n  qual review\n  qual setup\n", a.text("Ready. From your repository, review changes or propose project rules:"))
	}
	return a.line("%s\n", a.text("Nothing activated. Run qual init when you are ready."))
}
