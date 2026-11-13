package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestLongReadContinuationAvoidsRepeatedPaidCompaction(t *testing.T) {
	provider := &pricedProvider{}
	rt, state, p := requestFixture(t, provider)
	p.Catalog.ContextWindow = 1050000
	for i := range 8 {
		p.Messages = append(p.Messages, domain.Message{Role: "assistant", Content: fmt.Sprintf("Inspect relevant caller %d", i)}, domain.Message{Role: "tool", ToolCallID: fmt.Sprintf("read-%d", i), Content: strings.Repeat("source evidence line\n", 900)})
		if err := rt.compact(t.Context(), &state, p); err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls != 1 {
		t.Fatalf("eight related reads paid for %d summaries; want one", provider.calls)
	}
	if p.Messages[len(p.Messages)-1].ToolCallID != "read-7" {
		t.Fatal("recent evidence disappeared before continuation")
	}
	if p.Ledger.Reserved != 0 || p.Ledger.Spent != 142000 {
		t.Fatalf("summary settlement changed: %+v", p.Ledger)
	}
}

func TestLeaderCompactionPreservesExactPrimaryResults(t *testing.T) {
	provider := &pricedProvider{}
	rt, state, p := requestFixture(t, provider)
	p.Spec.Role = domain.RoleLeader
	state.Results = []domain.AgentResult{{AgentID: "primary", Findings: []domain.Finding{{ID: "primary-exact-id", ReviewerID: "primary", Description: "Every original detail must survive, not just a lossy summary"}}}}
	if err := initialMessages(&state, p); err != nil {
		t.Fatal(err)
	}
	original := p.Messages[2]
	rt.config.CompactThreshold = 1
	for range 2 {
		p.Messages = append(p.Messages, domain.Message{Role: "assistant", Content: "More investigation"})
		if err := rt.compact(t.Context(), &state, p); err != nil {
			t.Fatal(err)
		}
		if len(p.Messages) != 4 || p.Messages[2].Content != original.Content || p.Messages[2].Role != "user" {
			t.Fatal("compaction dropped or changed original primary results")
		}
	}
	if provider.calls != 2 {
		t.Fatalf("forced compactions: %d", provider.calls)
	}
}
