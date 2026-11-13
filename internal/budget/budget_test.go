package budget

import (
	"sync"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestExactMoneyAndConcurrentReservations(t *testing.T) {
	m, err := Parse("1.000000001")
	if err != nil || m != 1000000001 || Format(m) != "1.000000001" {
		t.Fatalf("money %d %v", m, err)
	}
	if _, err = Parse("1.0000000001"); err == nil {
		t.Fatal("lost precision")
	}
	l := New(domain.BudgetLimits{Hard: 10})
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < 30; i++ {
		wg.Go(func() {
			if _, e := l.Reserve(1); e == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if accepted != 10 {
		t.Fatalf("reserved %d", accepted)
	}
}

func TestUnknownCostNeverBecomesZero(t *testing.T) {
	l := New(domain.BudgetLimits{Hard: 100})
	id, err := l.Reserve(40)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Settle(id, domain.Usage{Known: true}); err != nil {
		t.Fatal(err)
	}
	got := l.Snapshot()
	if got.Unknown != 1 || got.Reserved != 40 || got.CostKnown {
		t.Fatalf("unknown cost erased: %+v", got)
	}
	restored := Restore(got)
	if _, err = restored.Reserve(61); err == nil {
		t.Fatal("unknown reservation spent twice")
	}
}

func TestNormalizedPriceAvoidsDoubleReasoningAndCache(t *testing.T) {
	p := domain.Pricing{Known: true, InputPerMillion: 1000000000, OutputPerMillion: 2000000000, CacheReadPerMillion: 100000000}
	cost, err := Price(domain.Usage{Known: true, InputTokens: 100, CacheReadTokens: 20, OutputTokens: 30, ReasoningTokens: 10}, p)
	if err != nil || cost != 142000 {
		t.Fatalf("price %d %v", cost, err)
	}
	cost, err = Price(domain.Usage{Known: true, InputTokens: 1}, domain.Pricing{Known: true, InputPerMillion: 1})
	if err != nil || cost != 1 {
		t.Fatalf("sub-nanodollar charge must round up %d %v", cost, err)
	}
}
