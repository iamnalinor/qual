package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestSelectedModelOutputLimit(t *testing.T) {
	provider := &pricedProvider{}
	rt, state, p := requestFixture(t, provider)
	p.Catalog.MaxOutputTokens = 256
	request := rt.agentRequest(&state, p)
	if request.MaxOutputTokens != 256 {
		t.Fatalf("small model received %d output tokens", request.MaxOutputTokens)
	}
}

func TestSmallContextCompactsBeforeGlobalThreshold(t *testing.T) {
	provider := &pricedProvider{}
	rt, state, p := requestFixture(t, provider)
	p.Catalog.MaxOutputTokens = 256
	p.Messages = append(p.Messages, domain.Message{Role: "assistant", Content: strings.Repeat("history ", 1000)}, domain.Message{Role: "user", Content: "continue with saved evidence"})
	req := rt.agentRequest(&state, p)
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) >= rt.config.CompactThreshold {
		t.Fatal("fixture exceeds global threshold")
	}
	// The full tool-bearing turn no longer fits, but a summary request can fit.
	p.Catalog.ContextWindow = len(payload) + 8192 + 256 - 1024
	needed, err := rt.needsCompact(p)
	if err != nil || !needed {
		t.Fatalf("small context missed compaction: %v %v", needed, err)
	}
	before := append([]domain.Message(nil), p.Messages...)
	if err = rt.compact(t.Context(), &state, p); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || len(p.Messages) != 3 || p.Messages[0].Content != before[0].Content {
		t.Fatalf("compaction lost contract or did not run: %d", provider.calls)
	}
	if !strings.Contains(p.Messages[2].Content, "full transcript artifact") {
		t.Fatal("lost evidence reference")
	}
}

func TestIrreducibleContextFailsBeforeProviderOrReservation(t *testing.T) {
	provider := &pricedProvider{}
	rt, state, p := requestFixture(t, provider)
	p.Catalog.MaxOutputTokens = 64
	p.Catalog.ContextWindow = 512
	if _, err := rt.request(context.Background(), &state, p, rt.agentRequest(&state, p)); domain.CodeOf(err) != domain.ErrInvalidConfig {
		t.Fatalf("wanted explicit contract limit failure, got %v", err)
	}
	if provider.calls != 0 || p.Ledger.Reserved != 0 {
		t.Fatal("impossible request dispatched or charged")
	}
}

type limitedProvider struct {
	pricedProvider
	last domain.ModelRequest
}

func (p *limitedProvider) Run(ctx context.Context, request domain.ModelRequest, emit func(domain.ModelEvent) error) error {
	p.last = request
	return p.pricedProvider.Run(ctx, request, emit)
}

func TestProviderRequestClampsVerifiedOutputLimit(t *testing.T) {
	provider := &limitedProvider{}
	rt, state, p := requestFixture(t, provider)
	p.Catalog.MaxOutputTokens = 128
	request := rt.agentRequest(&state, p)
	request.MaxOutputTokens = 8192
	if _, err := rt.request(t.Context(), &state, p, request); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || provider.last.MaxOutputTokens != 128 {
		t.Fatalf("provider received oversized output allowance: %d", provider.last.MaxOutputTokens)
	}
}

func TestOversizedCompactionCannotDispatchOrLoop(t *testing.T) {
	provider := &pricedProvider{}
	rt, state, p := requestFixture(t, provider)
	p.Catalog.MaxOutputTokens = 64
	p.Catalog.ContextWindow = 12000
	p.Messages = append(p.Messages, domain.Message{Role: "assistant", Content: strings.Repeat("large tool evidence", 3000)}, domain.Message{Role: "user", Content: "continue"})
	if err := rt.compact(t.Context(), &state, p); domain.CodeOf(err) != domain.ErrInvalidConfig {
		t.Fatalf("oversized summary request got %v", err)
	}
	if provider.calls != 0 || p.Ledger.Reserved != 0 {
		t.Fatal("impossible compaction dispatched")
	}
}
