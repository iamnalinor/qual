package app

import (
	"sort"
	"strings"

	"github.com/iamnalinor/qual/internal/config"
	"gopkg.in/yaml.v3"
)

// DisplayConfig renders reference-only settings or a concise validation result.
func (a *App) DisplayConfig(e config.Effective, validate bool) error {
	if err := a.selectUI(e); err != nil {
		return err
	}
	if a.JSON {
		return a.Output(e)
	}
	if validate {
		return a.line("%s: %d %s, %d %s, %d %s.\n", a.text("Configuration is valid"), len(e.Settings.Connections), a.text("connections"), len(e.Agents), a.text("roles"), len(e.Rules), a.text("scoped rules"))
	}
	if err := a.heading("Configuration"); err != nil {
		return err
	}
	data, err := yaml.Marshal(e.Settings)
	if err != nil {
		return err
	}
	if err = a.line("%s", data); err != nil {
		return err
	}
	return a.configProvenance(e)
}

func (a *App) configProvenance(e config.Effective) error {
	if err := a.line("\n%s\n", a.text("Provenance")); err != nil {
		return err
	}
	keys := make([]string, 0, len(e.Provenance))
	for key := range e.Provenance {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]tableRow, 0, len(keys))
	for _, key := range keys {
		origin := e.Provenance[key]
		location := origin.Path
		if origin.Revision != "" {
			location += " @ " + origin.Revision
		}
		rows = append(rows, tableRow{key, origin.Source, location})
	}
	if err := a.table(tableRow{a.text("FIELD"), a.text("SOURCE"), a.text("LOCATION")}, rows); err != nil {
		return err
	}
	if err := a.line("\n%s: %d\n", a.text("Scoped rules"), len(e.Rules)); err != nil {
		return err
	}
	for _, rule := range e.Rules {
		if err := a.line("  %s\n", rule.Path+" ("+rule.Source+", scope="+rule.Scope+")"); err != nil {
			return err
		}
	}
	if len(e.Warnings) > 0 {
		return a.line("\n%s\n", strings.Join(e.Warnings, "\n"))
	}
	return nil
}
