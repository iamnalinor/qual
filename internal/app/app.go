// Package app composes Qual adapters at the application edge.
package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/gitstore"
	"github.com/iamnalinor/qual/internal/platform"
	"github.com/iamnalinor/qual/internal/port"
	"github.com/iamnalinor/qual/internal/session"
	"github.com/iamnalinor/qual/internal/tui"
)

// App owns one invocation's persistent home and explicit streams.
type App struct {
	terminal             *tui.UI
	Home                 string
	UILanguage           string
	In                   io.Reader
	Out, Err             io.Writer
	JSON, NonInteractive bool
}

// New selects the home once, independently of a review's target.
func New(in io.Reader, out, diagnostic io.Writer) (*App, error) {
	home := os.Getenv("QUAL_HOME")
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(user, ".qual")
	}
	absolute, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	return &App{Home: absolute, In: in, Out: out, Err: diagnostic, UILanguage: os.Getenv("QUAL_UI_LANGUAGE")}, nil
}

func invalid(message string) error {
	return &domain.Error{Code: domain.ErrInvalidConfig, Message: message}
}

// Output writes a single machine-readable result to stdout.
func (a *App) Output(value any) error { return json.NewEncoder(a.Out).Encode(value) }

func (a *App) request(snapshot domain.Snapshot, overrides []byte) config.Request {
	env := map[string]string{}
	for _, name := range []string{"QUAL_UI_LANGUAGE", "QUAL_REVIEW_LANGUAGE"} {
		if value := os.Getenv(name); value != "" {
			env[name] = value
		}
	}
	return config.Request{Home: a.Home, Snapshot: snapshot, Environment: env, CLI: overrides}
}

func runID() (domain.RunID, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return domain.RunID(hex.EncodeToString(data[:])), nil
}

func (a *App) prepare(ctx context.Context, request domain.SourceRequest) (domain.Snapshot, *platform.Source, error) {
	source, err := a.source(ctx, request.Target)
	if err != nil {
		return domain.Snapshot{}, nil, err
	}
	id, err := runID()
	if err != nil {
		return domain.Snapshot{}, source, err
	}
	request.RunID = id
	request.Home = a.Home
	request.AllowEmpty = true
	snapshot, err := source.Prepare(ctx, request)
	if domain.CodeOf(err) == "no_changes" {
		err = nil
	}
	return snapshot, source, err
}

// Config resolves fixed-revision project settings when a target is provided.
func (a *App) Config(ctx context.Context, target string) (config.Effective, error) {
	var snapshot domain.Snapshot
	if target != "" {
		var err error
		snapshot, _, err = a.prepare(ctx, domain.SourceRequest{Target: target, AllowEmpty: true})
		if err != nil {
			return config.Effective{}, err
		}
	}
	return a.resolveConfig(ctx, a.request(snapshot, nil))
}

// Cleanup removes eligible disposable worktrees; a zero age includes all terminal entries.
func (a *App) Cleanup(ctx context.Context, ttl time.Duration) error {
	paths, err := gitstore.New(a.Home).Cleanup(ctx, time.Now(), ttl)
	if err != nil {
		return err
	}
	return a.renderCleanup(paths)
}

type preparedSource struct {
	port.Source
	snapshot domain.Snapshot
}

func (p preparedSource) Prepare(context.Context, domain.SourceRequest) (domain.Snapshot, error) {
	return p.snapshot, nil
}

type observedStore struct {
	*session.Store
	app         *App
	effective   config.Effective
	source      *platform.Source
	roles       map[domain.AgentID]roleView
	progressErr error
	progressMu  sync.Mutex
}

func (s *observedStore) Create(ctx context.Context, state domain.RunState) error {
	if err := s.Store.Create(ctx, state); err != nil {
		return err
	}
	if err := s.artifact(ctx, state.RunID, "effective-config.json", s.effective); err != nil {
		return err
	}
	if err := s.artifact(ctx, state.RunID, "execution-environment.json", executionEnvironment()); err != nil {
		return err
	}
	if state.Snapshot.Kind == "github" || state.Snapshot.Kind == "gitlab" {
		metadata, err := s.source.Context(state.Snapshot)
		if err != nil {
			return err
		}
		return s.artifact(ctx, state.RunID, "platform.json", metadata)
	}
	return nil
}

func (s *observedStore) artifact(ctx context.Context, id domain.RunID, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	artifact, err := s.PutArtifact(ctx, id, name, bytes.NewReader(data))
	if err != nil {
		return err
	}
	event, err := json.Marshal(map[string]string{"name": name, "artifact_id": artifact.ID})
	if err != nil {
		return err
	}
	return s.Store.Append(ctx, domain.Event{SchemaVersion: 1, RunID: id, Type: "application_artifact", Time: time.Now().UTC(), Data: event})
}

func (s *observedStore) Append(ctx context.Context, event domain.Event) error {
	if err := s.progressError(); err != nil {
		return err
	}
	if err := s.Store.Append(ctx, event); err != nil {
		return err
	}
	if event.Type == "budget_threshold" || event.Type == "budget_phase" {
		return nil
	}
	return s.progress(event)
}

func (a *App) retain(ctx context.Context, state domain.RunState) error {
	var result error
	source := gitstore.New(a.Home)
	now := time.Now().UTC()
	for _, agent := range state.Agents {
		err := source.Retain(context.WithoutCancel(ctx), state.RunID, agent.ID, &now, false)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *observedStore) progressError() error {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.progressErr
}
