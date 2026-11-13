package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/port"
	"github.com/iamnalinor/qual/internal/session"
)

type pricedProvider struct {
	calls int
	fail  bool
}

func (*pricedProvider) Models(context.Context) ([]domain.ModelInfo, error) {
	return []domain.ModelInfo{{ID: "priced", Pricing: domain.Pricing{Known: true, InputPerMillion: 1000000000, OutputPerMillion: 2000000000, CacheReadPerMillion: 100000000}}}, nil
}

func (p *pricedProvider) Run(_ context.Context, _ domain.ModelRequest, emit func(domain.ModelEvent) error) error {
	p.calls++
	if p.fail {
		return &domain.Error{Code: domain.ErrTool, Retryable: true, HTTPStatus: 429, RetryAfter: time.Second, Message: "fixture capacity"}
	}
	usage := domain.Usage{Known: true, InputTokens: 100, OutputTokens: 30, ReasoningTokens: 10, CacheReadTokens: 20}
	if err := emit(domain.ModelEvent{Type: "usage", Usage: &usage}); err != nil {
		return err
	}
	// Providers may repeat a cumulative usage record; settle the final total once.
	if err := emit(domain.ModelEvent{Type: "usage", Usage: &usage}); err != nil {
		return err
	}
	return emit(domain.ModelEvent{Type: "text", Text: "Investigation continues."})
}

func requestFixture(t *testing.T, provider port.Provider) (*Runtime, domain.RunState, *agentProgress) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	state := domain.RunState{RunID: "request", Snapshot: domain.Snapshot{RunID: "request"}}
	rt := New(Config{Home: home, Store: session.New(home), Source: diskSource{root: home}, Providers: map[string]port.Provider{"fixture": provider}})
	if err := rt.config.Store.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	p := &agentProgress{Spec: domain.AgentSpec{ID: "agent", Role: domain.RoleReviewer, Provider: "fixture", Model: "priced"}}
	if err := rt.initialize(ctx, &state, p); err != nil {
		t.Fatal(err)
	}
	return rt, state, p
}

func TestPricedUsageAndSavedResponseAreNotChargedTwice(t *testing.T) {
	provider := &pricedProvider{}
	rt, state, p := requestFixture(t, provider)
	ctx := context.Background()
	req := domain.ModelRequest{Model: "priced", MaxOutputTokens: 8192, Messages: p.Messages, Tools: rt.definitions()}
	if _, err := rt.request(ctx, &state, p, req); err != nil {
		t.Fatal(err)
	}
	if p.Ledger.Spent != 142000 || p.Ledger.Reserved != 0 || p.Usage.Cost != 142000 {
		t.Fatalf("incorrect settled ledger: %+v %+v", p.Ledger, p.Usage)
	}
	restored, err := rt.recover(ctx, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.request(ctx, &state, restored[p.Spec.ID], req); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("repeated paid response: %d calls", provider.calls)
	}
}

func TestRetryBudgetIsTenTotalAndPreservesUnknownCharges(t *testing.T) {
	provider := &pricedProvider{fail: true}
	rt, state, p := requestFixture(t, provider)
	waits := 0
	rt.wait = func(_ context.Context, delay time.Duration) error {
		if delay < time.Second {
			t.Fatalf("ignored Retry-After: %v", delay)
		}
		waits++
		return nil
	}
	_, err := rt.request(context.Background(), &state, p, domain.ModelRequest{Model: "priced", MaxOutputTokens: 8192})
	if err == nil || provider.calls != 10 || waits != 9 {
		t.Fatalf("retry count %d waits %d: %v", provider.calls, waits, err)
	}
	if p.Ledger.Unknown != 10 || p.Ledger.Reserved == 0 || p.Usage.CostKnown {
		t.Fatalf("lost retry charges: %+v", p.Ledger)
	}
}
