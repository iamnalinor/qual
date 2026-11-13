package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/platform"
	"github.com/iamnalinor/qual/internal/port"
	agentruntime "github.com/iamnalinor/qual/internal/runtime"
	"github.com/iamnalinor/qual/internal/session"
	"github.com/iamnalinor/qual/internal/tui"
)

// ReviewOptions controls snapshot selection and independently configurable role guidance.
type ReviewOptions struct {
	Source                                                                    domain.SourceRequest
	ExpectedHead, ExpectedTarget, Output                                      string
	Focus, Instructions, Reasoning, Investigation, UILanguage, ReviewLanguage string
	FailOnCritical                                                            bool
}

// Review prepares one snapshot, records effective configuration, then executes the common runtime.
func (a *App) Review(ctx context.Context, o ReviewOptions) error {
	snapshot, source, err := a.prepare(ctx, o.Source)
	if err != nil {
		return err
	}
	if err = checkExpected(snapshot, o); err != nil {
		return err
	}
	overrides, err := reviewOverrides(o)
	if err != nil {
		return err
	}
	effective, err := a.resolveConfig(ctx, a.request(snapshot, overrides))
	if err != nil {
		return err
	}
	if err = a.prepareUI(effective); err != nil {
		return err
	}
	if err = applyReviewOptions(&effective, o); err != nil {
		return err
	}
	store := &observedStore{Store: session.New(a.Home), app: a, effective: effective, source: source}
	if len(snapshot.Files) == 0 {
		state := domain.RunState{SchemaVersion: 1, RunID: snapshot.RunID, Snapshot: snapshot, Status: domain.StatusNoChanges, CreatedAt: time.Now().UTC()}
		if err = store.Create(ctx, state); err != nil {
			return err
		}
		return a.finish(ctx, state, o.Output, false)
	}
	if len(effective.Agents) == 0 {
		return invalid("Connect a provider and choose a reviewer and leader first: run qual init. Then run qual review from your repository.")
	}
	if err := a.checkCredentials(ctx, effective, effective.Agents); err != nil {
		return err
	}
	return a.executeReview(ctx, snapshot, source, store, effective, o)
}

func (a *App) executeReview(ctx context.Context, snapshot domain.Snapshot, source *platform.Source, store *observedStore, effective config.Effective, o ReviewOptions) error {
	agents := guidance(effective.Agents, effective)
	var state domain.RunState
	var closeRuntime func() error
	runErr := a.withTerminal(ctx, a.text("Review")+" · "+o.Source.Target, func(ctx context.Context) error {
		a.sessionHeader(snapshot, effective.Agents)
		runtime, closeFn, e := a.runtime(ctx, snapshot, source, store, effective)
		if e != nil {
			return e
		}
		closeRuntime = closeFn
		state, e = runtime.Review(ctx, domain.SourceRequest{RunID: snapshot.RunID}, agents)
		return e
	})
	if closeRuntime == nil {
		return runErr
	}
	closeErr := closeRuntime()
	retainErr := a.retain(ctx, state)
	return errors.Join(runErr, closeErr, retainErr, a.finish(ctx, state, o.Output, o.FailOnCritical))
}

func reviewOverrides(o ReviewOptions) ([]byte, error) {
	values := map[string]any{}
	if o.UILanguage != "" {
		values["ui_language"] = o.UILanguage
	}
	if o.ReviewLanguage != "" {
		values["review_language"] = o.ReviewLanguage
	}
	return json.Marshal(values)
}

func (a *App) runtime(ctx context.Context, snapshot domain.Snapshot, source *platform.Source, store *observedStore, e config.Effective) (*agentruntime.Runtime, func() error, error) {
	service, err := a.mcpService(e, store, snapshot.RunID)
	if err != nil {
		return nil, nil, err
	}
	if err = service.Connect(ctx); err != nil {
		return nil, nil, errors.Join(err, service.Close())
	}
	tools := service.Tools()
	r := agentruntime.New(agentruntime.Config{Source: preparedSource{Source: source, snapshot: snapshot}, Store: store, Providers: a.providers(e), Home: a.Home, Tools: tools, Ask: a.ask, ToolPath: trustedToolPath(), OnEvent: store.budgetEvent})
	return r, func() error { return errors.Join(service.Close(), store.progressError()) }, nil
}

func (a *App) ask(ctx context.Context, question string) (string, error) {
	if a.terminal != nil {
		return a.terminal.Ask(ctx, tui.Prompt{Title: a.text("Question"), Description: a.text(question)})
	}
	if err := a.question(question); err != nil {
		return "", err
	}
	return "", &domain.Error{Code: domain.ErrUnsupportedCapability, Message: a.text("User input unavailable in noninteractive mode")}
}

var _ port.Source = preparedSource{}

func applyReviewOptions(e *config.Effective, o ReviewOptions) error {
	for i := range e.Settings.Reviewers {
		applyReviewer(&e.Settings.Reviewers[i], o)
	}
	if err := config.Validate(e.Settings); err != nil {
		return err
	}
	for i := range e.Agents {
		if e.Agents[i].Role != domain.RoleReviewer {
			continue
		}
		for _, configured := range e.Settings.Reviewers {
			if domain.AgentID(configured.ID) != e.Agents[i].ID {
				continue
			}
			agent := &e.Agents[i]
			agent.Focus = configured.Focus
			agent.Instructions = configured.Instructions
			agent.ReasoningEffort = configured.Reasoning
			agent.InvestigationEffort = configured.Investigation
		}
	}
	if o.Focus != "" || o.Instructions != "" || o.Reasoning != "" || o.Investigation != "" {
		e.Provenance["reviewers"] = config.Origin{Source: "cli"}
	}
	return nil
}

func applyReviewer(agent *config.Agent, o ReviewOptions) {
	if o.Focus != "" {
		agent.Focus = o.Focus
	}
	if o.Instructions != "" {
		agent.Instructions += "\n" + o.Instructions
	}
	if o.Reasoning != "" {
		agent.Reasoning = o.Reasoning
	}
	if o.Investigation != "" {
		agent.Investigation = o.Investigation
	}
}

func checkExpected(snapshot domain.Snapshot, o ReviewOptions) error {
	if o.ExpectedHead != "" && snapshot.OriginalHead != o.ExpectedHead {
		return &domain.Error{Code: "ci_snapshot", Message: "prepared source head differs from authorized head"}
	}
	if o.ExpectedTarget != "" && snapshot.TargetCommit != o.ExpectedTarget {
		return &domain.Error{Code: "ci_snapshot", Message: "prepared target differs from authorized target"}
	}
	return nil
}

func trustedToolPath() string {
	if os.Getenv("QUAL_SANDBOX_ACTIVE") == "1" {
		return os.Getenv("PATH")
	}
	return ""
}
