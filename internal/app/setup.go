package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/platform"
	"github.com/iamnalinor/qual/internal/session"
	"github.com/iamnalinor/qual/internal/tui"
	"gopkg.in/yaml.v3"
)

// SetupOptions requires explicit acceptance before changing personal settings.
type SetupOptions struct {
	Target, Trust, Output string
	Accept                bool
}

// Setup investigates via the same runtime and validates proposals before personal persistence.
func (a *App) Setup(ctx context.Context, o SetupOptions) error {
	snapshot, source, err := a.prepare(ctx, domain.SourceRequest{Target: o.Target, AllowEmpty: true})
	if err != nil {
		return err
	}
	initial, err := a.resolveConfig(ctx, a.request(snapshot, nil))
	if err != nil {
		return err
	}
	if err = a.prepareUI(initial); err != nil {
		return err
	}
	if initial.Settings.Setup.ID == "" {
		return invalid("Project setup needs a provider account and setup model. Run qual init first, then qual setup.")
	}
	setupSpec, err := config.ResolveSetup(initial.Settings)
	if err != nil {
		return fmt.Errorf("run qual init to choose a setup model: %w", err)
	}
	if err = a.checkCredentials(ctx, initial, []domain.AgentSpec{setupSpec}); err != nil {
		return err
	}
	trust, err := a.chooseTrust(ctx, o.Trust)
	if err != nil {
		return err
	}
	overrides, err := json.Marshal(map[string]string{"trust": trust})
	if err != nil {
		return err
	}
	request := a.request(snapshot, overrides)
	e, err := a.resolveConfig(ctx, request)
	if err != nil {
		return err
	}
	spec, err := config.ResolveSetup(e.Settings)
	if err != nil {
		return fmt.Errorf("choose a setup model with qual init before project setup: %w", err)
	}
	spec = guidance([]domain.AgentSpec{spec}, e)[0]
	store := &observedStore{Store: session.New(a.Home), app: a, effective: e, source: source}
	return a.executeSetup(ctx, snapshot, source, e, spec, store, request, o, trust)
}

func (a *App) executeSetup(ctx context.Context, snapshot domain.Snapshot, source *platform.Source, e config.Effective, spec domain.AgentSpec, store *observedStore, request config.Request, o SetupOptions, trust string) error {
	var state domain.RunState
	var closeRuntime func() error
	runErr := a.withTerminal(ctx, a.text("Project setup")+" · "+o.Target, func(ctx context.Context) error {
		a.sessionHeader(snapshot, []domain.AgentSpec{spec})
		runtime, closeFn, e := a.runtime(ctx, snapshot, source, store, e)
		if e != nil {
			return e
		}
		closeRuntime = closeFn
		state, e = runtime.Setup(ctx, domain.SourceRequest{RunID: snapshot.RunID}, spec)
		return e
	})
	if closeRuntime == nil {
		return runErr
	}
	if err := errors.Join(runErr, closeRuntime(), a.retain(ctx, state), a.finish(ctx, state, o.Output, false)); err != nil {
		return err
	}
	return a.setupAcceptance(ctx, state, request, o, trust)
}

func (a *App) applySetup(ctx context.Context, state domain.RunState, request config.Request, o SetupOptions, trust string) (bool, error) {
	proposal, err := setupProposal(state)
	if err != nil {
		return false, err
	}
	preview, err := config.Preview(proposal)
	if err != nil {
		return false, err
	}
	if err = a.proposal(ctx, "Proposed personal settings and rules", filepath.Join(a.Home, "projects", string(state.Snapshot.ProjectID)), preview); err != nil {
		return false, err
	}
	accepted, err := a.accept(ctx, o.Accept, "Apply these personal settings and rules? (yes/no):")
	if err != nil {
		return false, err
	}
	return accepted, config.Apply(ctx, config.ApplyRequest{Home: a.Home, ProjectID: string(state.Snapshot.ProjectID), Context: request, Proposal: proposal, Accepted: accepted, Trust: trust})
}

func (a *App) chooseTrust(ctx context.Context, trust string) (string, error) {
	if trust == "" && a.interactive() {
		err := a.withTerminal(ctx, a.text("Project setup"), func(ctx context.Context) error {
			var err error
			trust, err = a.choose(ctx, "How should Qual treat this project?", "Untrusted projects cannot authorize external tools or provider connections.", []tui.Option{{Label: a.text("Untrusted"), Description: a.text("Use personal settings and built-in review tools. Recommended for unfamiliar code."), Value: "untrusted"}, {Label: a.text("Trusted"), Description: a.text("Allow explicitly configured project integrations."), Value: "trusted"}})
			return err
		})
		return trust, err
	}
	if trust == "" {
		trust = "untrusted"
	}

	if trust != "trusted" && trust != "untrusted" {
		return "", invalid("trust must be trusted or untrusted")
	}
	return trust, nil
}

func (a *App) accept(ctx context.Context, explicit bool, prompt string) (bool, error) {
	accepted := explicit
	if !explicit && a.terminal != nil {
		answer, err := a.choose(ctx, prompt, "", []tui.Option{{Label: a.text("Apply settings"), Value: "yes"}, {Label: a.text("Keep preview only"), Value: "no"}})
		if err != nil {
			return false, err
		}
		accepted = answer == "yes"
	}

	return accepted, a.acceptance(accepted)
}

func setupProposal(state domain.RunState) (config.Proposal, error) {
	var proposal config.Proposal
	if len(state.Results) != 1 || state.Status != domain.StatusCompleted {
		return proposal, &domain.Error{Code: domain.ErrIncomplete, Message: "setup did not produce a validated finish"}
	}
	artifacts := state.Results[0].Artifacts
	if len(artifacts) != 4 {
		return proposal, invalid("setup proposal artifact missing")
	}
	data, err := os.ReadFile(artifacts[3].Path)
	if err != nil {
		return proposal, err
	}
	var value struct {
		Settings map[string]any `json:"settings"`
		Rules    []string       `json:"rules"`
	}
	if err = json.Unmarshal(data, &value); err != nil {
		return proposal, err
	}
	proposal.Settings, err = yaml.Marshal(value.Settings)
	if err != nil {
		return proposal, err
	}
	proposal.Rules = map[string]string{}
	for i, rule := range value.Rules {
		proposal.Rules[fmt.Sprintf("setup-%03d.md", i+1)] = rule
	}
	return proposal, nil
}

// InitOptions describes a connection and default models without credential values.
type InitOptions struct {
	GitLabHost, GitLabTokenEnv                                                                    string
	GitLabPublic                                                                                  bool
	Device                                                                                        bool
	Capabilities                                                                                  []string
	Connection, Kind, Endpoint, Credential, Model, LeaderModel, SetupModel, Reasoning, Soft, Hard string
	UILanguage, ReviewLanguage                                                                    string
	Accept                                                                                        bool
	AddOnly                                                                                       bool
}

// Init checks the explicit route before atomically activating secret-free user settings.
func (a *App) Init(ctx context.Context, o InitOptions) error {
	if err := validateInitPlatformOptions(o); err != nil {
		return err
	}
	if err := a.globalUI(ctx); err != nil {
		return err
	}
	var settings config.Settings
	var accepted bool
	var host string
	err := a.withTerminal(ctx, a.text("Set up Qual"), func(ctx context.Context) error {
		var err error
		if a.terminal != nil && o.GitLabHost == "" && o.Kind == "" {
			o.Kind, err = a.providerChoice(ctx)
			if err != nil {
				return err
			}
		}
		if o.GitLabHost != "" || o.Kind == "gitlab-host" {
			host, accepted, err = a.configureGitLabHost(ctx, o)
			return err
		}
		settings, accepted, err = a.initialize(ctx, o)
		return err
	})
	if err != nil {
		return err
	}
	if host != "" {
		return a.renderGitLabHost(host, accepted)
	}
	return a.renderInit(accepted, settings)
}

func (a *App) initialize(ctx context.Context, o InitOptions) (config.Settings, bool, error) {
	o, err := a.initChoices(ctx, o)
	if err != nil {
		return config.Settings{}, false, err
	}
	if !initComplete(o) {
		return config.Settings{}, false, invalid("Start qual init in a terminal to choose a provider and model, or supply --connection, --kind, --model and --credential for automation.")
	}
	existing, err := a.Config(ctx, "")
	if err != nil {
		return config.Settings{}, false, err
	}
	settings := mergeInitSettings(existing.Settings, initialSettings(o), o.AddOnly)
	if err = config.Validate(settings); err != nil {
		return settings, false, err
	}
	if a.terminal != nil {
		a.terminal.Activity(tui.Activity{Summary: a.text("Checking account access · no inference request")})
	}
	if err = a.checkConnection(ctx, settings.Connections[o.Connection], o.Model, defaultModel(o.LeaderModel, o.Model), defaultModel(o.SetupModel, o.Model)); err != nil {
		return settings, false, err
	}
	accepted, err := a.activateSettings(ctx, o, settings)
	return settings, accepted, err
}

func (a *App) activateSettings(ctx context.Context, o InitOptions, settings config.Settings) (bool, error) {
	data, err := yaml.Marshal(settings)
	if err != nil {
		return false, err
	}
	preview := data
	if a.terminal != nil {
		preview = a.initPreview(o, settings)
	}
	if err = a.proposal(ctx, "User settings proposal", filepath.Join(a.Home, "settings.yaml"), preview); err != nil {
		return false, err
	}
	accepted, err := a.accept(ctx, o.Accept, "Activate these settings? (yes/no):")
	if err != nil {
		return false, err
	}
	if accepted {
		err = writePrivate(filepath.Join(a.Home, "settings.yaml"), data)
	}
	return accepted, err
}

func (a *App) initPreview(o InitOptions, settings config.Settings) []byte {
	rows := []tableRow{{a.text("Provider"), o.Kind}, {a.text("Account"), strings.TrimPrefix(o.Credential, "profile:")}, {a.text("Verified model"), o.Model}, {a.text("Reviewer"), a.previewRole(settings.Reviewers[0])}, {a.text("Leader"), a.previewRole(settings.Leader)}, {a.text("Setup model"), a.previewRole(settings.Setup)}, {a.text("Interface"), settings.UILanguage}, {a.text("Reports"), settings.ReviewLanguage}, {a.text("Project trust"), settings.Trust}}
	var preview strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&preview, "%s: %s\n", row[0], row[1])
	}
	return []byte(preview.String())
}

func initialSettings(o InitOptions) config.Settings {
	if o.LeaderModel == "" {
		o.LeaderModel = o.Model
	}
	if o.SetupModel == "" {
		o.SetupModel = o.Model
	}
	agent := func(id, model string) config.Agent {
		return config.Agent{ID: id, Provider: o.Connection, Model: model, Reasoning: o.Reasoning, Soft: o.Soft, Hard: o.Hard}
	}
	return config.Settings{SchemaVersion: 1, UILanguage: o.UILanguage, ReviewLanguage: o.ReviewLanguage, Trust: "untrusted", Connections: map[string]config.Connection{o.Connection: {Kind: o.Kind, Endpoint: o.Endpoint, Credential: o.Credential, Capabilities: o.Capabilities}}, Reviewers: []config.Agent{agent("reviewer", o.Model)}, Leader: agent("leader", o.LeaderModel), Setup: agent("setup", o.SetupModel)}
}

func (a *App) initChoices(ctx context.Context, o InitOptions) (InitOptions, error) {
	if a.terminal != nil && (o.Model == "" || o.Credential == "") {
		return a.guidedInit(ctx, o)
	}
	if o.Connection == "" {
		o.Connection = "primary"
	}
	if o.Kind == "" {
		o.Kind = "openai"
	}
	return o, nil
}

func initComplete(o InitOptions) bool { return o.Connection != "" && o.Model != "" }

func defaultModel(selected, fallback string) string {
	if selected != "" {
		return selected
	}
	return fallback
}

func (a *App) setupAcceptance(ctx context.Context, state domain.RunState, request config.Request, o SetupOptions, trust string) error {
	var accepted bool
	err := a.withTerminal(ctx, a.text("Project setup proposal"), func(ctx context.Context) error {
		var err error
		accepted, err = a.applySetup(ctx, state, request, o, trust)
		return err
	})
	if err != nil || a.JSON {
		return err
	}
	if !accepted {
		return a.line("%s\n", a.text("Proposal declined. Personal settings and rules were not changed."))
	}
	root := filepath.Join(a.Home, "projects", string(state.Snapshot.ProjectID))
	return a.FilePaths(map[string]string{"settings": filepath.Join(root, "settings.yaml"), "rules": filepath.Join(root, "rules")})
}

func (a *App) previewRole(role config.Agent) string {
	threshold := a.text("Unlimited")
	if role.Hard != "" {
		threshold = "$" + role.Hard + " " + a.text("stop threshold")
	}
	return role.Model + " · " + threshold
}
