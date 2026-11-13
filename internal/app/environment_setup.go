package app

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
)

func (a *App) resolveConfig(ctx context.Context, request config.Request) (config.Effective, error) {
	effective, err := config.Resolve(ctx, request)
	if err != nil {
		return effective, configurationError(effective, err)
	}
	return effective, nil
}

func configurationError(e config.Effective, cause error) error {
	agents := append([]config.Agent{}, e.Settings.Reviewers...)
	agents = append(agents, e.Settings.Leader, e.Settings.Setup)
	for _, agent := range agents {
		if agent.ID == "" || agent.Provider == "" {
			continue
		}
		if _, ok := e.Settings.Connections[agent.Provider]; ok {
			continue
		}
		origin := e.Provenance["reviewers"]
		if agent.ID == e.Settings.Leader.ID {
			origin = e.Provenance["leader.model"]
		}
		if agent.ID == e.Settings.Setup.ID {
			origin = e.Provenance["setup.model"]
		}
		message := fmt.Sprintf("Model %s for %s requires provider account %s, which is not configured locally (selected by %s). Configure its credentials with qual init --connection %s --model %s --add-account, then rerun this command.", agent.Model, agent.ID, agent.Provider, originLabel(origin), commandArgument(agent.Provider), commandArgument(agent.Model))
		return &domain.Error{Code: domain.ErrInvalidConfig, Message: message, Cause: cause}
	}
	return cause
}

func originLabel(origin config.Origin) string {
	label := origin.Source
	if origin.Path != "" {
		label += " " + origin.Path
	}
	if origin.Revision != "" {
		label += " @ " + origin.Revision
	}
	if label == "" {
		return "effective settings; inspect qual config show"
	}
	return label
}

func commandArgument(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_-. /:", r) || r == ' '
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func mergeInitSettings(existing, selected config.Settings, addOnly bool) config.Settings {
	if existing.Connections == nil {
		existing.Connections = map[string]config.Connection{}
	}
	for id, connection := range selected.Connections {
		existing.Connections[id] = connection
	}
	if !addOnly {
		existing.Reviewers = selected.Reviewers
		existing.Leader = selected.Leader
		existing.Setup = selected.Setup
	}
	existing.SchemaVersion = 1
	if existing.Trust == "" {
		existing.Trust = "untrusted"
	}
	if selected.UILanguage != "" {
		existing.UILanguage = selected.UILanguage
	}
	if selected.ReviewLanguage != "" {
		existing.ReviewLanguage = selected.ReviewLanguage
	}
	return existing
}

func (a *App) initConnection(ctx context.Context, o InitOptions) (string, error) {
	existing, err := a.Config(ctx, "")
	if err != nil {
		return "", err
	}
	for _, id := range connectionNames(existing) {
		c := existing.Settings.Connections[id]
		if c.Kind == o.Kind && c.Endpoint == o.Endpoint && c.Credential == o.Credential {
			return id, nil
		}
	}
	id := o.Kind
	for n := 2; existing.Settings.Connections[id].Kind != ""; n++ {
		id = fmt.Sprintf("%s-%d", o.Kind, n)
	}
	return id, nil
}

func modelOrigin(e config.Effective, agent domain.AgentSpec) config.Origin {
	field := "reviewers"
	if agent.Role == domain.RoleLeader {
		field = "leader.model"
	}
	if agent.Role == domain.RoleSetup {
		field = "setup.model"
	}
	return e.Provenance[field]
}

func (a *App) checkCredentials(ctx context.Context, e config.Effective, agents []domain.AgentSpec) error {
	for _, agent := range agents {
		connection := e.Settings.Connections[agent.Provider]
		p := lazyProvider{app: a, connection: connection, name: agent.Provider, origin: originLabel(modelOrigin(e, agent))}
		if _, err := p.adapter(ctx); err != nil {
			return p.requestError(agent.Model, err, false)
		}
	}
	return nil
}
