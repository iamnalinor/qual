package runtime

import (
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func tieredModel() domain.ModelInfo {
	return domain.ModelInfo{Pricing: domain.Pricing{Known: true, InputPerMillion: 100000000, OutputPerMillion: 500000000, CacheReadPerMillion: 10000000, CacheWritePerMillion: 125000000}, PricingTiers: []domain.ContextPricing{{AboveInputTokens: 272000, Pricing: domain.Pricing{Known: true, InputPerMillion: 200000000, OutputPerMillion: 750000000, CacheReadPerMillion: 20000000, CacheWritePerMillion: 250000000}}}}
}

func TestContextTierSettlementUsesInclusiveInputBoundary(t *testing.T) {
	for _, test := range []struct {
		input int64
		want  domain.Money
	}{{272000, 27200000}, {272001, 54400200}} {
		answer := response{usage: domain.Usage{Known: true, InputTokens: test.input}}
		if err := priceResponse(&answer, tieredModel()); err != nil {
			t.Fatal(err)
		}
		if !answer.usage.CostKnown || answer.usage.Cost != test.want {
			t.Fatalf("input %d price %d known %v; want %d", test.input, answer.usage.Cost, answer.usage.CostKnown, test.want)
		}
	}
	answer := response{usage: domain.Usage{Known: true, InputTokens: 272001, CacheReadTokens: 72001, OutputTokens: 100, ReasoningTokens: 40}}
	if err := priceResponse(&answer, tieredModel()); err != nil {
		t.Fatal(err)
	}
	if answer.usage.Cost != 41515020 {
		t.Fatalf("tier cache/reasoning double counted: %d", answer.usage.Cost)
	}
}

func TestReservationUsesWorstApplicableContextRates(t *testing.T) {
	model := tieredModel()
	pricing := reservationPricing(model, 272000)
	if pricing.InputPerMillion != 100000000 || pricing.OutputPerMillion != 500000000 {
		t.Fatalf("base boundary used tier: %+v", pricing)
	}
	pricing = reservationPricing(model, 272001)
	if pricing.InputPerMillion != 200000000 || pricing.OutputPerMillion != 750000000 || pricing.CacheWritePerMillion != 250000000 {
		t.Fatalf("tier reservation underpriced: %+v", pricing)
	}
	model.PricingTiers = append(model.PricingTiers, domain.ContextPricing{AboveInputTokens: 400000, Pricing: domain.Pricing{Known: false}})
	if reservationPricing(model, 400001).Known {
		t.Fatal("unknown applicable tier fabricated a known reservation")
	}
	answer := response{usage: domain.Usage{Known: true, InputTokens: 400001}}
	if err := priceResponse(&answer, model); err != nil {
		t.Fatal(err)
	}
	if answer.usage.CostKnown {
		t.Fatal("unknown actual tier fabricated known zero cost")
	}
}
