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
	agentruntime "github.com/iamnalinor/qual/internal/runtime"
	"github.com/iamnalinor/qual/internal/session"
)

// Sessions lists persisted checkpoints, without private continuation data.
func (a *App) Sessions(ctx context.Context) error {
	if err := a.globalUI(ctx); err != nil {
		return err
	}
	states, err := session.New(a.Home).List(ctx)
	if err != nil {
		return err
	}
	return a.renderSessions(states)
}

// Show renders the original persisted session state.
func (a *App) Show(ctx context.Context, id domain.RunID) error {
	if err := a.globalUI(ctx); err != nil {
		return err
	}
	store := session.New(a.Home)
	state, err := store.Load(ctx, id)
	if err != nil {
		return sessionError(id, err)
	}
	events, err := store.Events(ctx, id)
	if err != nil {
		return err
	}
	history, err := visibleHistory(events)
	if err != nil {
		return err
	}
	return a.renderSession(state, history)
}

// Cancel signals an owner in another process and marks the session cancelled.
func (a *App) Cancel(ctx context.Context, id domain.RunID) error {
	store := session.New(a.Home)
	r := agentruntime.New(agentruntime.Config{Store: store})
	if err := r.Cancel(ctx, id); err != nil {
		return sessionError(id, err)
	}
	return a.confirmation("Session cancellation requested", id, map[string]any{"run_id": id, "status": domain.StatusCancelled})
}

// Export writes the versioned portable diagnostic archive, preserving the private checkpoint.
func (a *App) Export(ctx context.Context, id domain.RunID, path string) error {
	if path == "" && a.interactive() {
		return invalid("Choose where to save this diagnostic archive: qual sessions export " + string(id) + " --output session.json")
	}
	if path == "" {
		return session.New(a.Home).Export(ctx, id, a.Out)
	}
	var data strings.Builder
	if err := session.New(a.Home).Export(ctx, id, &data); err != nil {
		return err
	}
	if err := writePrivate(path, []byte(data.String())); err != nil {
		return err
	}
	return a.FilePaths(map[string]string{"archive": path})
}

// Resume resolves current credentials against the immutable stored configuration and role specs.
func (a *App) Resume(ctx context.Context, id domain.RunID, output string) error {
	store := session.New(a.Home)
	state, err := store.Load(ctx, id)
	if err != nil {
		return sessionError(id, err)
	}
	effective, err := storedEffective(ctx, store, id)
	if err != nil {
		return err
	}
	if err = a.selectUI(effective); err != nil {
		return err
	}
	source, err := a.source(ctx, state.Snapshot.Repository)
	if err != nil {
		return err
	}
	observed := &observedStore{Store: store, app: a, effective: effective, source: source}
	var result domain.RunState
	var closeRuntime func() error
	runErr := a.withTerminal(ctx, a.text("Resume")+" · "+string(id), func(ctx context.Context) error {
		a.sessionHeader(state.Snapshot, state.Agents)
		runtime, closeFn, e := a.runtime(ctx, state.Snapshot, source, observed, effective)
		if e != nil {
			return e
		}
		closeRuntime = closeFn
		result, e = runtime.Resume(ctx, id)
		return e
	})
	if closeRuntime == nil {
		return runErr
	}
	return errors.Join(runErr, closeRuntime(), a.retain(ctx, result), a.finish(ctx, result, output, false))
}

func storedEffective(ctx context.Context, store *session.Store, id domain.RunID) (config.Effective, error) {
	var result config.Effective
	events, err := store.Events(ctx, id)
	if err != nil {
		return result, err
	}
	for _, event := range events {
		if event.Type != "application_artifact" {
			continue
		}
		var metadata struct {
			Name string `json:"name"`
			ID   string `json:"artifact_id"`
		}
		if err = json.Unmarshal(event.Data, &metadata); err != nil {
			return result, err
		}
		if metadata.Name != "effective-config.json" {
			continue
		}
		artifact, loadErr := store.Artifact(ctx, id, metadata.ID)
		if loadErr != nil {
			return result, loadErr
		}
		data, readErr := os.ReadFile(artifact.Path)
		if readErr != nil {
			return result, readErr
		}
		return result, json.Unmarshal(data, &result)
	}
	return result, invalid("session is missing its immutable effective configuration")
}

func (a *App) finish(ctx context.Context, state domain.RunState, output string, failCritical bool) error {
	if state.RunID == "" {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	markdown := renderMarkdown(state)
	store := session.New(a.Home)
	if _, err = store.PutArtifact(ctx, state.RunID, "report.json", strings.NewReader(string(data))); err != nil {
		return err
	}
	report, err := store.PutArtifact(ctx, state.RunID, "report.md", strings.NewReader(markdown))
	if err != nil {
		return err
	}
	if err = saveReports(output, data, markdown); err != nil {
		return err
	}
	if a.JSON {
		err = a.Output(state)
	} else {
		if a.interactive() {
			err = a.terminalResult(state, report.Path, output)
		} else {
			_, err = fmt.Fprintf(a.Out, "%s", markdown)
		}
	}
	if err != nil {
		return err
	}
	return resultError(state, failCritical)
}

func renderMarkdown(state domain.RunState) string {
	var text strings.Builder
	fmt.Fprintf(&text, "# Qual review\n\nSession: `%s`\n\nStatus: `%s`\n\nSnapshot: `%s` → `%s`\n\n", state.RunID, state.Status, state.Snapshot.BaseCommit, state.Snapshot.HeadCommit)
	for _, role := range state.Results {
		fmt.Fprintf(&text, "## %s (%s)\n\n%s\n\nUsage: input %d, output %d; cost known: %t; cost nanodollars: %d.\n\n", role.AgentID, role.Status, role.Report, role.Usage.InputTokens, role.Usage.OutputTokens, role.Usage.CostKnown, role.Usage.Cost)
		for _, question := range role.Questions {
			fmt.Fprintf(&text, "Question: %s\n\n", question)
		}
		for _, assumption := range role.Assumptions {
			fmt.Fprintf(&text, "Assumption: %s\n\n", assumption)
		}
	}
	return text.String()
}

func saveReports(output string, data []byte, markdown string) error {
	if output == "" {
		return nil
	}
	if err := writePrivate(filepath.Join(output, "report.json"), data); err != nil {
		return err
	}
	return writePrivate(filepath.Join(output, "report.md"), []byte(markdown))
}

func resultError(state domain.RunState, failCritical bool) error {
	if state.Status != domain.StatusCompleted && state.Status != domain.StatusNoChanges {
		return &domain.Error{Code: domain.ErrIncomplete, Message: "run ended without complete review"}
	}
	if err := completeRoles(state.Results); err != nil {
		return err
	}
	if !failCritical {
		return nil
	}
	for _, role := range state.Results {
		if role.Role != domain.RoleLeader {
			continue
		}
		for _, finding := range role.Findings {
			if finding.Severity == domain.SeverityCritical {
				return &domain.Error{Code: "critical_findings", Message: "leader confirmed critical findings"}
			}
		}
	}
	return nil
}

func completeRoles(results []domain.AgentResult) error {
	for _, role := range results {
		if role.Status != domain.StatusCompleted {
			return &domain.Error{Code: domain.ErrIncomplete, Message: "a primary reviewer did not produce a validated finish"}
		}
	}
	return nil
}

func sessionError(id domain.RunID, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return &domain.Error{Code: domain.ErrInvalidConfig, Message: "Session " + string(id) + " was not found. Run qual sessions list to choose a saved session."}
	}
	return err
}
