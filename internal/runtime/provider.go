package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/iamnalinor/qual/internal/budget"
	"github.com/iamnalinor/qual/internal/domain"
)

type response struct {
	text     string
	calls    []domain.ToolCall
	usage    domain.Usage
	state    []byte
	observed bool
}

func (r *Runtime) request(ctx context.Context, state *domain.RunState, p *agentProgress, req domain.ModelRequest) (response, error) {
	if p.Response != nil {
		if err := r.afterBudget(ctx, state, p); err != nil {
			return response{}, err
		}
		return response{text: p.Response.Text, calls: p.Response.Calls}, nil
	}
	var answer response
	for attempt := 0; attempt < 10; attempt++ {
		var err error
		answer, err = r.attempt(ctx, state, p, req)
		if err == nil {
			return answer, nil
		}
		if !retryable(err) || answer.observed || attempt == 9 {
			return answer, err
		}
		if err = r.backoff(ctx, attempt, err); err != nil {
			return answer, err
		}
	}
	return answer, coded(domain.ErrIncomplete, "retry budget exhausted", nil)
}

func (r *Runtime) attempt(ctx context.Context, state *domain.RunState, p *agentProgress, req domain.ModelRequest) (response, error) {
	var answer response
	clampOutput(&req, p.Catalog)
	if err := r.prepareBudget(ctx, state, p, &req); err != nil {
		return answer, err
	}
	clampOutput(&req, p.Catalog)
	if err := requireContext(req, p.Catalog); err != nil {
		return answer, err
	}
	ledger := budget.Restore(p.Ledger)
	estimate, err := r.estimate(p, req)
	if err != nil {
		return answer, err
	}
	reservation, err := ledger.Reserve(estimate)
	if err != nil {
		return answer, err
	}
	p.Ledger = ledger.Snapshot()
	if err = r.checkpoint(ctx, state.RunID, p); err != nil {
		return answer, err
	}
	answer, callErr := r.invoke(ctx, p, req)
	if err = priceResponse(&answer, p.Catalog); err != nil {
		return answer, err
	}
	if err = ledger.Settle(reservation, answer.usage); err != nil {
		return answer, err
	}
	p.Ledger = ledger.Snapshot()
	addUsage(&p.Usage, answer.usage)
	return r.saveResponse(ctx, state, p, answer, callErr, len(req.Tools) == 0)
}

func priceResponse(answer *response, model domain.ModelInfo) error {
	pricing := settlementPricing(model, answer.usage.InputTokens)
	if answer.usage.CostKnown || !answer.usage.Known || !pricing.Known {
		return nil
	}
	cost, err := budget.Price(answer.usage, pricing)
	if err != nil {
		return err
	}
	answer.usage.Cost = cost
	answer.usage.CostKnown = true
	return nil
}

func (r *Runtime) invoke(ctx context.Context, p *agentProgress, req domain.ModelRequest) (response, error) {
	ctx, cancel := context.WithTimeout(ctx, r.config.ModelTimeout)
	defer cancel()
	var answer response
	var text strings.Builder
	err := r.config.Providers[p.Spec.Provider].Run(ctx, req, func(event domain.ModelEvent) error {
		switch event.Type {
		case "text":
			text.WriteString(event.Text)
			answer.observed = true
		case "tool_call":
			if event.ToolCall != nil {
				answer.calls = append(answer.calls, *event.ToolCall)
				answer.observed = true
			}
		case "usage":
			if event.Usage != nil {
				answer.usage = *event.Usage
			}
		case "completed":
		default:
		}
		if len(event.ProviderState) > 0 {
			answer.state = append(answer.state[:0], event.ProviderState...)
		}
		if event.Error != nil {
			return event.Error
		}
		return ctx.Err()
	})
	answer.text = text.String()
	return answer, err
}

func retryable(err error) bool {
	var e *domain.Error
	if !errors.As(err, &e) {
		return false
	}
	if e.Code == domain.ErrAuth || e.Code == domain.ErrInvalidConfig || e.Code == domain.ErrUnsupportedCapability {
		return false
	}
	return e.Retryable || e.HTTPStatus == 429 || e.HTTPStatus >= 500 && e.HTTPStatus <= 599
}

func (r *Runtime) backoff(ctx context.Context, attempt int, cause error) error {
	capDelay := time.Duration(1<<attempt) * 100 * time.Millisecond
	if capDelay > 10*time.Second {
		capDelay = 10 * time.Second
	}
	jitter, err := rand.Int(rand.Reader, big.NewInt(int64(capDelay/2)+1))
	if err != nil {
		return fmt.Errorf("retry jitter: %w", err)
	}
	delay := capDelay/2 + time.Duration(jitter.Int64())
	var e *domain.Error
	if errors.As(cause, &e) && e.RetryAfter > delay {
		delay = e.RetryAfter
	}
	return r.wait(ctx, delay)
}

func waitDuration(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func addUsage(total *domain.Usage, next domain.Usage) {
	// Unknown counters and actual monetary charges are separate signals.
	total.InputTokens += next.InputTokens
	total.OutputTokens += next.OutputTokens
	total.ReasoningTokens += next.ReasoningTokens
	total.CacheReadTokens += next.CacheReadTokens
	total.CacheWriteTokens += next.CacheWriteTokens
	total.Cost += next.Cost
	total.Known = total.Known && next.Known
	total.CostKnown = total.CostKnown && next.CostKnown
	total.Billing = next.Billing
}

func (r *Runtime) catalog(ctx context.Context, p *agentProgress) error {
	provider := r.config.Providers[p.Spec.Provider]
	if provider == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.config.ModelTimeout)
	defer cancel()
	models, err := provider.Models(ctx)
	if err != nil {
		return err
	}
	for _, model := range models {
		if model.ID == p.Spec.Model {
			p.Catalog = model
			return nil
		}
	}
	return nil
}

func (r *Runtime) estimate(p *agentProgress, request domain.ModelRequest) (domain.Money, error) {
	amount, known, err := r.estimateDetails(p.Catalog, request)
	if !known {
		p.ApproximateReservation = true
	}
	return amount, err
}

func (r *Runtime) estimateDetails(model domain.ModelInfo, request domain.ModelRequest) (domain.Money, bool, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return 0, false, fmt.Errorf("measure request: %w", err)
	}
	// UTF-8 bytes plus envelope allowance conservatively exceed ordinary token counts.
	input := int64(len(payload)) + requestProtocolAllowance
	pricing := reservationPricing(model, input)
	if !pricing.Known {
		return r.config.RequestReserve, false, nil
	}
	rate := pricing.InputPerMillion
	if pricing.CacheReadPerMillion > rate {
		rate = pricing.CacheReadPerMillion
	}
	if pricing.CacheWritePerMillion > rate {
		rate = pricing.CacheWritePerMillion
	}
	pricing.InputPerMillion = rate
	amount, err := budget.Price(domain.Usage{Known: true, InputTokens: input, OutputTokens: int64(request.MaxOutputTokens)}, pricing)
	return amount, true, err
}

func settlementPricing(model domain.ModelInfo, input int64) domain.Pricing {
	pricing := model.Pricing
	threshold := int64(-1)
	for _, tier := range model.PricingTiers {
		if input > tier.AboveInputTokens && tier.AboveInputTokens > threshold {
			pricing = tier.Pricing
			threshold = tier.AboveInputTokens
		}
	}
	return pricing
}

func reservationPricing(model domain.ModelInfo, inputBound int64) domain.Pricing {
	pricing := model.Pricing
	for _, tier := range model.PricingTiers {
		if inputBound <= tier.AboveInputTokens {
			continue
		}
		pricing.Known = pricing.Known && tier.Pricing.Known
		pricing.InputPerMillion = max(pricing.InputPerMillion, tier.Pricing.InputPerMillion)
		pricing.OutputPerMillion = max(pricing.OutputPerMillion, tier.Pricing.OutputPerMillion)
		pricing.CacheReadPerMillion = max(pricing.CacheReadPerMillion, tier.Pricing.CacheReadPerMillion)
		pricing.CacheWritePerMillion = max(pricing.CacheWritePerMillion, tier.Pricing.CacheWritePerMillion)
	}
	return pricing
}

func (r *Runtime) saveResponse(ctx context.Context, state *domain.RunState, p *agentProgress, answer response, callErr error, summary bool) (response, error) {
	var err error
	if len(answer.state) > 0 || callErr == nil {
		p.ProviderState = append(json.RawMessage(nil), answer.state...)
	}
	if callErr == nil {
		p.Response = &savedResponse{Text: answer.text, Calls: answer.calls, Summary: summary}
	}
	if err = r.checkpoint(context.WithoutCancel(ctx), state.RunID, p); err != nil {
		return answer, errors.Join(callErr, err)
	}
	if err = r.afterBudget(context.WithoutCancel(ctx), state, p); err != nil {
		return answer, errors.Join(callErr, err)
	}
	return answer, errors.Join(callErr, r.checkpoint(context.WithoutCancel(ctx), state.RunID, p))
}
