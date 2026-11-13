package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const selectedModel = "gpt-6-luna"

type ledger struct {
	Schema         int               `json:"schema_version"`
	Currency       string            `json:"currency"`
	Ceiling        int64             `json:"ceiling_nanodollars"`
	Spent          int64             `json:"spent_nanodollars"`
	Reserved       int64             `json:"reserved_nanodollars"`
	Model          string            `json:"selected_model"`
	CheckedAt      string            `json:"pricing_checked_at"`
	Source         string            `json:"pricing_source"`
	InputRate      int64             `json:"input_per_million_nanodollars"`
	CachedRate     int64             `json:"cached_input_per_million_nanodollars"`
	CacheWriteRate int64             `json:"cache_write_per_million_nanodollars"`
	OutputRate     int64             `json:"output_per_million_nanodollars"`
	Records        []json.RawMessage `json:"records"`
}

type record struct {
	ID          string `json:"attempt_id"`
	Status      string `json:"status"`
	Model       string `json:"model"`
	Reservation int64  `json:"reservation_nanodollars"`
	RequestID   string `json:"request_id,omitempty"`
	Input       int64  `json:"input_tokens,omitempty"`
	Cached      int64  `json:"cached_input_tokens,omitempty"`
	Output      int64  `json:"output_tokens,omitempty"`
	Cost        int64  `json:"cost_nanodollars,omitempty"`
}

func (l *ledger) validate() error {
	if l.Schema != 1 || l.Currency != "USD" || l.Model != selectedModel || l.Ceiling <= 0 || l.Ceiling > 10000000000 {
		return errors.New("invalid ledger identity or ceiling")
	}
	if l.Spent < 0 || l.Reserved < 0 || l.Spent > l.Ceiling || l.Reserved > l.Ceiling-l.Spent {
		return errors.New("invalid ledger balances")
	}
	return l.validateReceipt()
}

func (l *ledger) validateReceipt() error {
	if l.InputRate != 100000000 || l.CachedRate != 10000000 || l.CacheWriteRate != 125000000 || l.OutputRate != 500000000 || l.CheckedAt != "2026-11-13" || l.Source != "https://developers.openai.com/api/docs/models/gpt-6-luna" || len(l.Records) == 0 {
		return errors.New("missing verified tariff receipt")
	}
	return l.validateHistory()
}

func updateLedger(ctx context.Context, path string, change func(*ledger) error) (err error) {
	// Ledger path is explicitly selected by the developer, outside the target sandbox.
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // Developer-selected ledger path, never model input.
	if err != nil {
		return fmt.Errorf("open budget lock: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err = lockFile(ctx, lock); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unix.Flock(int(lock.Fd()), unix.LOCK_UN)) }()
	data, err := os.ReadFile(path) //nolint:gosec // Developer-selected ledger path, never model input.
	if err != nil {
		return fmt.Errorf("read existing budget: %w", err)
	}
	var l ledger
	if err = json.Unmarshal(data, &l); err != nil {
		return fmt.Errorf("decode budget: %w", err)
	}
	if err = l.validate(); err != nil {
		return err
	}
	if err = change(&l); err != nil {
		return err
	}
	if err = l.validate(); err != nil {
		return err
	}
	data, err = json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encode budget: %w", err)
	}
	return replaceLedger(path, append(data, '\n'))
}

func lockFile(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return fmt.Errorf("lock budget: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func replaceLedger(path string, data []byte) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".live-budget-*")
	if err != nil {
		return fmt.Errorf("create budget replacement: %w", err)
	}
	defer func() {
		if e := os.Remove(file.Name()); e != nil && !errors.Is(e, os.ErrNotExist) { //nolint:gosec // Temp file created by this function in developer-selected ledger directory.
			err = errors.Join(err, e)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil { //nolint:gosec // Atomic replacement of developer-selected ledger path.
		return err
	}
	dir, err := os.Open(filepath.Dir(path)) //nolint:gosec // Sync developer-selected ledger directory.
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func (l *ledger) addRecord(r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	l.Records = append(l.Records, data)
	return nil
}

func (l *ledger) validateHistory() error {
	if err := validateAvailabilityReceipt(l.Records[0]); err != nil {
		return err
	}
	pending := map[string]record{}
	seen := map[string]bool{}
	var spent, reserved int64
	for _, raw := range l.Records[1:] {
		var rec record
		if json.Unmarshal(raw, &rec) != nil {
			return errors.New("invalid attempt record")
		}
		if err := validateRecord(rec, pending, seen); err != nil {
			return err
		}
		if rec.Status == "reserved" {
			reserved += rec.Reservation
		} else {
			reserved -= rec.Reservation
			spent += rec.Cost
		}
		if reserved < 0 || reserved > l.Ceiling || spent > l.Ceiling-reserved {
			return errors.New("attempt record ceiling exceeded")
		}
	}
	if spent != l.Spent || reserved != l.Reserved {
		return errors.New("ledger balances disagree with attempt records")
	}
	return nil
}

func validateRecord(rec record, pending map[string]record, seen map[string]bool) error {
	if rec.ID == "" || rec.Model != selectedModel || rec.Reservation <= 0 || rec.Reservation > 10000000000 {
		return errors.New("invalid attempt identity or reservation")
	}
	switch rec.Status {
	case "reserved":
		if seen[rec.ID] {
			return errors.New("duplicate reservation")
		}
		seen[rec.ID] = true
		pending[rec.ID] = rec
	case "settled":
		if err := validateSettlement(rec, pending); err != nil {
			return err
		}
		delete(pending, rec.ID)
	default:
		return errors.New("unknown attempt status")
	}
	return nil
}

func validateAvailabilityReceipt(raw json.RawMessage) error {
	var receipt struct {
		Operation string `json:"operation"`
		Available bool   `json:"model_available"`
		Requests  int    `json:"inference_requests"`
		Cost      int64  `json:"cost_nanodollars"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Operation != "models.list" || !receipt.Available || receipt.Requests != 0 || receipt.Cost != 0 {
		return errors.New("invalid original availability receipt")
	}
	return nil
}

func validateSettlement(rec record, pending map[string]record) error {
	previous, ok := pending[rec.ID]
	if !ok || previous.Reservation != rec.Reservation {
		return errors.New("settlement missing reservation")
	}
	if rec.Cost < 0 || rec.Cost > rec.Reservation || rec.RequestID == "" {
		return errors.New("invalid settlement cost or request ID")
	}
	return nil
}
