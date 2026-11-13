// Package budget provides exact nanodollar accounting and atomic role reservations.
package budget

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/iamnalinor/qual/internal/domain"
)

// Parse reads nonnegative USD without rounding sub-nanodollar amounts.
func Parse(value string) (domain.Money, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, fmt.Errorf("invalid USD %q", value)
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) > 9 {
			return 0, fmt.Errorf("USD precision exceeds nanodollars")
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse USD: %w", err)
	}
	if !decimalDigits(fraction) {
		return 0, fmt.Errorf("invalid USD fraction")
	}
	frac, err := strconv.ParseInt(fraction+strings.Repeat("0", 9-len(fraction)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse fraction: %w", err)
	}
	if whole > (math.MaxInt64-frac)/1000000000 {
		return 0, fmt.Errorf("USD overflow")
	}
	return domain.Money(whole*1000000000 + frac), nil
}

// Format renders all nine decimal places exactly.
func Format(m domain.Money) string { return fmt.Sprintf("%d.%09d", m/1000000000, m%1000000000) }

// State is a persisted ledger. Unknown settlements retain their reservation.
type State struct {
	Limits       domain.BudgetLimits     `json:"limits"`
	Spent        domain.Money            `json:"spent"`
	Reserved     domain.Money            `json:"reserved"`
	Unknown      int                     `json:"unknown"`
	CostKnown    bool                    `json:"cost_known"`
	Next         uint64                  `json:"next"`
	Reservations map[uint64]domain.Money `json:"reservations"`
}

// Ledger serializes concurrent reservation and settlement decisions.
type Ledger struct {
	mu    sync.Mutex
	state State
}

// New starts a role ledger.
func New(limits domain.BudgetLimits) *Ledger { return Restore(State{Limits: limits, CostKnown: true}) }

// Restore reconstructs outstanding reservations without resetting money.
func Restore(s State) *Ledger { s.Reservations = clone(s.Reservations); return &Ledger{state: s} }

func clone(m map[uint64]domain.Money) map[uint64]domain.Money {
	result := make(map[uint64]domain.Money, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// Snapshot is independent of subsequent changes.
func (l *Ledger) Snapshot() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state
	s.Reservations = clone(s.Reservations)
	return s
}

// Reserve atomically checks a projected charge against the hard limit.
func (l *Ledger) Reserve(amount domain.Money) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := &l.state
	if amount < 0 || s.Spent > domain.Money(math.MaxInt64)-s.Reserved || amount > domain.Money(math.MaxInt64)-s.Spent-s.Reserved {
		return 0, budgetError("reservation overflow")
	}
	if s.Limits.Hard > 0 && s.Spent+s.Reserved+amount > s.Limits.Hard {
		return 0, budgetError("role hard limit reached")
	}
	s.Next++
	s.Reserved += amount
	s.Reservations[s.Next] = amount
	return s.Next, nil
}

// Settle replaces an estimate only when the actual charge is known.
func (l *Ledger) Settle(id uint64, u domain.Usage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := &l.state
	amount, ok := s.Reservations[id]
	if !ok {
		return budgetError("unknown reservation")
	}
	if !u.CostKnown {
		s.Unknown++
		s.CostKnown = false
		return nil
	}
	if u.Cost < 0 || u.Cost > domain.Money(math.MaxInt64)-s.Spent {
		return budgetError("settlement overflow")
	}
	delete(s.Reservations, id)
	s.Reserved -= amount
	s.Spent += u.Cost
	return nil
}

func budgetError(message string) error {
	return &domain.Error{Code: domain.ErrBudget, Message: message}
}

// Price calculates normalized usage; reasoning is already part of output tokens.
func Price(u domain.Usage, p domain.Pricing) (domain.Money, error) {
	if !u.Known || !p.Known {
		return 0, budgetError("price or usage unknown")
	}
	counts := []int64{u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens}
	rates := []domain.Money{p.InputPerMillion, p.OutputPerMillion, p.CacheReadPerMillion, p.CacheWritePerMillion}
	var total int64
	for i, count := range counts {
		rate := int64(rates[i])
		if count < 0 || rate < 0 || rate != 0 && count > math.MaxInt64/rate {
			return 0, budgetError("pricing overflow or invalid usage")
		}
		product := count * rate
		if product > math.MaxInt64-total {
			return 0, budgetError("pricing overflow")
		}
		total += product
	}
	rounded := total / 1000000
	if total%1000000 != 0 {
		rounded++
	}
	return domain.Money(rounded), nil
}

func decimalDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
