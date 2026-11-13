// Package runtime owns role orchestration and the provider/tool agent lifecycle.
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode"

	"github.com/iamnalinor/qual/internal/budget"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/port"
)

// Config supplies adapters only at the application edge.
type Config struct {
	OnEvent   func(domain.Event)
	Source    port.Source
	Store     port.Store
	Providers map[string]port.Provider
	Home      string
	// ToolPath is supplied only by the trusted caller; empty uses fixed system paths.
	ToolPath         string
	Tools            []port.Tool
	Ask              func(context.Context, string) (string, error)
	MaxTurns         int
	MaxOutputBytes   int
	MaxArtifactBytes int64
	RequestReserve   domain.Money
	ModelTimeout     time.Duration
	CompactThreshold int
}

// Runtime executes setup, reviewer and leader through the same loop.
type Runtime struct {
	wait   func(context.Context, time.Duration) error
	config Config
	mu     sync.Mutex
	active map[domain.RunID]context.CancelFunc
}

// New sets conservative bounded defaults without starting work.
func New(config Config) *Runtime {
	if config.ModelTimeout == 0 {
		config.ModelTimeout = 3 * time.Minute
	}
	if config.MaxTurns == 0 {
		config.MaxTurns = 100
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = 32 * 1024
	}
	if config.MaxArtifactBytes == 0 {
		config.MaxArtifactBytes = 4 * 1024 * 1024
	}
	if config.RequestReserve == 0 {
		config.RequestReserve = 1000000
	}
	if config.CompactThreshold == 0 {
		config.CompactThreshold = 96 * 1024
	}
	return &Runtime{wait: waitDuration, config: config, active: map[domain.RunID]context.CancelFunc{}}
}

func coded(code domain.ErrorCode, message string, cause error) error {
	return &domain.Error{Code: code, Message: message, Cause: cause}
}

func newID() (domain.RunID, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("create run ID: %w", err)
	}
	return domain.RunID(hex.EncodeToString(id[:])), nil
}

// Review captures one immutable source snapshot and runs every primary before its leader.
func (r *Runtime) Review(ctx context.Context, request domain.SourceRequest, agents []domain.AgentSpec) (domain.RunState, error) {
	var state domain.RunState
	if err := r.validate(agents, false); err != nil {
		return state, err
	}
	if request.RunID == "" {
		id, err := newID()
		if err != nil {
			return state, err
		}
		request.RunID = id
	}
	snapshot, err := r.config.Source.Prepare(ctx, request)
	if err != nil && domain.CodeOf(err) != "no_changes" {
		return state, err
	}
	snapshot.RunID = request.RunID
	state = domain.RunState{SchemaVersion: 1, RunID: request.RunID, Snapshot: snapshot, Agents: ordered(agents), Status: domain.StatusRunning, CreatedAt: time.Now().UTC()}
	if len(snapshot.Files) == 0 {
		state.Status = domain.StatusNoChanges
	}
	if err = r.config.Store.Create(ctx, state); err != nil {
		return state, err
	}
	if state.Status == domain.StatusNoChanges {
		return state, nil
	}
	return r.execute(ctx, state)
}

// Setup investigates through the identical loop; accepted proposal application belongs to the caller.
func (r *Runtime) Setup(ctx context.Context, request domain.SourceRequest, agent domain.AgentSpec) (domain.RunState, error) {
	var state domain.RunState
	agent.Role = domain.RoleSetup
	if err := r.validate([]domain.AgentSpec{agent}, true); err != nil {
		return state, err
	}
	if request.RunID == "" {
		id, err := newID()
		if err != nil {
			return state, err
		}
		request.RunID = id
	}
	request.AllowEmpty = true
	snapshot, err := r.config.Source.Prepare(ctx, request)
	if err != nil {
		return state, err
	}
	snapshot.RunID = request.RunID
	state = domain.RunState{SchemaVersion: 1, RunID: request.RunID, Snapshot: snapshot, Agents: []domain.AgentSpec{agent}, Status: domain.StatusRunning, CreatedAt: time.Now().UTC()}
	if err = r.config.Store.Create(ctx, state); err != nil {
		return state, err
	}
	return r.execute(ctx, state)
}

// Resume reuses the original run, snapshot, journal and ledger.
func (r *Runtime) Resume(ctx context.Context, id domain.RunID) (domain.RunState, error) {
	state, err := r.config.Store.Load(ctx, id)
	if err != nil {
		return state, err
	}
	if state.Status == domain.StatusCompleted || state.Status == domain.StatusNoChanges {
		return state, nil
	}
	return r.execute(ctx, state)
}

func ordered(agents []domain.AgentSpec) []domain.AgentSpec {
	result := make([]domain.AgentSpec, 0, len(agents))
	for _, a := range agents {
		if a.Role != domain.RoleLeader {
			result = append(result, a)
		}
	}
	for _, a := range agents {
		if a.Role == domain.RoleLeader {
			result = append(result, a)
		}
	}
	return result
}

func (r *Runtime) validate(agents []domain.AgentSpec, setup bool) error {
	if r.config.Source == nil || r.config.Store == nil {
		return coded(domain.ErrInvalidConfig, "source and store required", nil)
	}
	leaders, err := r.validateAgents(agents, setup)
	if err != nil {
		return err
	}
	if len(agents) == 0 || (!setup && (leaders != 1 || len(agents) < 2)) {
		return coded(domain.ErrInvalidConfig, "review needs primary reviewers and exactly one leader", nil)
	}
	return nil
}

func safeID(id string) bool {
	for _, c := range id {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '-' && c != '_' {
			return false
		}
	}
	return id != ""
}

func (r *Runtime) validateAgent(a domain.AgentSpec, setup bool) error {
	if r.config.Providers[a.Provider] == nil {
		return coded(domain.ErrInvalidConfig, "agent provider unavailable", nil)
	}
	if a.Role != domain.RoleReviewer && a.Role != domain.RoleLeader && (!setup || a.Role != domain.RoleSetup) {
		return coded(domain.ErrInvalidConfig, "invalid agent role", nil)
	}
	return validateBudget(a)
}

type locker interface {
	Lock(context.Context, domain.RunID) (func() error, error)
}

func (r *Runtime) execute(ctx context.Context, state domain.RunState) (result domain.RunState, err error) {
	if l, ok := r.config.Store.(locker); ok {
		unlock, e := l.Lock(ctx, state.RunID)
		if e != nil {
			return state, e
		}
		defer func() { err = errors.Join(err, unlock()) }()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if e := r.watchCancel(ctx, state.RunID, cancel); e != nil {
		return state, e
	}
	r.mu.Lock()
	r.active[state.RunID] = cancel
	r.mu.Unlock()
	defer r.forget(state.RunID)
	return r.roles(ctx, state)
}

func (r *Runtime) roles(ctx context.Context, state domain.RunState) (domain.RunState, error) {
	progress, e := r.recover(ctx, state.RunID)
	if e != nil {
		return state, e
	}
	state.Results = nil
	for _, spec := range state.Agents {
		p := progress[spec.ID]
		if p == nil {
			p = &agentProgress{Spec: spec, Ledger: budget.New(spec.Budget).Snapshot()}
		}
		if p.Finished != nil {
			state.Results = append(state.Results, *p.Finished)
			if spec.Role != domain.RoleReviewer && p.Finished.Status != domain.StatusCompleted {
				state.Status = p.Finished.Status
				return r.saveResult(ctx, state, coded(domain.ErrIncomplete, "role has no validated finish", nil))
			}
			continue
		}
		agentResult, runErr := r.runAgent(ctx, &state, p)
		state.Results = append(state.Results, agentResult)
		if runErr != nil && spec.Role != domain.RoleReviewer {
			state.Status = statusOf(runErr)
			return r.saveResult(ctx, state, runErr)
		}
		if ctx.Err() != nil {
			state.Status = domain.StatusCancelled
			return r.saveResult(ctx, state, ctx.Err())
		}
	}
	return r.finalize(ctx, state)
}

func (r *Runtime) finalize(ctx context.Context, state domain.RunState) (domain.RunState, error) {
	state.Status = domain.StatusPartial
	stale, e := r.config.Source.Stale(ctx, state.Snapshot)
	if e != nil {
		state.Status = statusOf(e)
		return r.saveResult(ctx, state, e)
	}
	if stale {
		state.Status = domain.StatusStaleSnapshot
		return r.saveResult(ctx, state, coded(domain.ErrStale, "source changed during review", nil))
	}
	state.Status = domain.StatusCompleted
	return r.saveResult(ctx, state, nil)
}
func (r *Runtime) forget(id domain.RunID) { r.mu.Lock(); defer r.mu.Unlock(); delete(r.active, id) }
func statusOf(err error) domain.Status {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return domain.StatusCancelled
	}
	if domain.CodeOf(err) == domain.ErrBudget {
		return domain.StatusBudgetStopped
	}
	return domain.StatusPartial
}

func (r *Runtime) saveResult(ctx context.Context, state domain.RunState, prior error) (domain.RunState, error) {
	state.UpdatedAt = time.Now().UTC()
	events, err := r.config.Store.Events(context.WithoutCancel(ctx), state.RunID)
	if err == nil {
		state.JournalSequence = int64(len(events))
	}
	return state, errors.Join(prior, err, r.config.Store.Save(context.WithoutCancel(ctx), state))
}

// Cancel stops processes owned by an active local runtime and persists cancellation.
func (r *Runtime) Cancel(ctx context.Context, id domain.RunID) error {
	r.mu.Lock()
	cancel := r.active[id]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if store, ok := r.config.Store.(cancelStore); ok {
		if err := store.RequestCancel(ctx, id); err != nil {
			return err
		}
	}
	state, err := r.config.Store.Load(ctx, id)
	if err != nil {
		return err
	}
	state.Status = domain.StatusCancelled
	return r.config.Store.Save(ctx, state)
}

func (r *Runtime) validateAgents(agents []domain.AgentSpec, setup bool) (int, error) {
	seen := map[domain.AgentID]bool{}
	leaders := 0
	for _, a := range agents {
		if a.ID == "" || !safeID(string(a.ID)) || seen[a.ID] {
			return 0, coded(domain.ErrInvalidConfig, "empty, unsafe or duplicate agent ID", nil)
		}
		seen[a.ID] = true
		if err := r.validateAgent(a, setup); err != nil {
			return 0, err
		}
		if a.Role == domain.RoleLeader {
			leaders++
		}
	}
	return leaders, nil
}

func validateBudget(a domain.AgentSpec) error {
	if a.Budget.Hard > 0 && a.Budget.Soft > 0 && (a.Budget.Hard < a.Budget.Soft || a.Role == domain.RoleLeader && a.Budget.Hard == a.Budget.Soft) {
		return coded(domain.ErrInvalidConfig, "hard budget must cover soft; leader hard must exceed soft", nil)
	}
	if a.Budget.Hard < 0 || a.Budget.Soft < 0 {
		return coded(domain.ErrInvalidConfig, "negative budget", nil)
	}
	return nil
}
