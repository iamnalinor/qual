package app

import (
	"context"
	"strings"

	"github.com/iamnalinor/qual/internal/auth"
	"github.com/iamnalinor/qual/internal/tui"
)

func (a *App) accountOptions(ctx context.Context, kind string, profiles []auth.Profile) ([]tui.Option, error) {
	var options []tui.Option
	seen := map[string]bool{}
	for _, profile := range profiles {
		seen[profile.ID] = true
		if profile.Kind != kind {
			continue
		}
		label := profile.Email
		if label == "" {
			label = profile.ID
		}
		options = append(options, tui.Option{Label: label, Description: a.text("Saved account") + " · " + kind, Value: "use/" + profile.ID}, a.reconnectOption(label, profile.ID))
		seen[profile.ID] = true
	}
	existing, err := a.Config(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, name := range connectionNames(existing) {
		connection := existing.Settings.Connections[name]
		id, profile := strings.CutPrefix(connection.Credential, "profile:")
		if connection.Kind != kind || !profile || seen[id] {
			continue
		}
		option := a.reconnectOption(name, id)
		if oauthKind(kind) {
			option = tui.Option{Label: a.text("Register account again") + ": " + name, Description: a.text("The saved OAuth registration is missing. Sign in to create a new registration for future runs."), Value: "new"}
		}
		options = append(options, option)
		seen[id] = true
	}
	return options, nil
}

func (a *App) reconnectOption(label, id string) tui.Option {
	return tui.Option{Label: a.text("Sign in again") + ": " + label, Description: a.text("Restore this account's credentials so saved sessions can resume."), Value: "reconnect/" + id}
}

func newAccountOptions(kind string, profiles []auth.Profile, device bool) LoginOptions {
	options := LoginOptions{Kind: kind, Device: device}
	if !oauthKind(kind) {
		options.Profile = uniqueProfile(kind, profiles)
	}
	return options
}

func (a *App) reservedAccountIDs(ctx context.Context, profiles []auth.Profile) ([]auth.Profile, error) {
	existing, err := a.Config(ctx, "")
	if err != nil {
		return nil, err
	}
	reserved := append([]auth.Profile{}, profiles...)
	for _, connection := range existing.Settings.Connections {
		if id, ok := strings.CutPrefix(connection.Credential, "profile:"); ok {
			reserved = append(reserved, auth.Profile{ID: id})
		}
	}
	return reserved, nil
}
