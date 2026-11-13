package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	maxBody           = 128 * 1024
	protocolAllowance = 8192
)

type gate struct {
	path, token, key, upstream string
	client                     *http.Client
}

func newGate(path, token, key, upstream string, client *http.Client) *gate {
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &gate{path: path, token: token, key: key, upstream: upstream, client: &copyClient}
}

type requestBody struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	Store     *bool  `json:"store"`
	MaxOutput int64  `json:"max_output_tokens"`
	Tools     []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      *bool           `json:"strict,omitempty"`
	} `json:"tools"`
	Input        json.RawMessage `json:"input"`
	Include      json.RawMessage `json:"include"`
	Reasoning    json.RawMessage `json:"reasoning"`
	Instructions json.RawMessage `json:"instructions"`
}

func validateRequest(data []byte) (requestBody, error) {
	var body requestBody
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return body, errors.New("invalid JSON")
	}
	if body.Model != selectedModel || !body.Stream || body.Store == nil || *body.Store || body.MaxOutput < 1 || body.MaxOutput > 8192 {
		return body, errors.New("request outside live policy")
	}
	for _, tool := range body.Tools {
		if tool.Type != "function" {
			return body, errors.New("hosted tools forbidden")
		}
	}
	if err := validateInput(body.Input); err != nil {
		return body, err
	}
	return body, nil
}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.URL.RawQuery != "" {
		http.Error(w, "endpoint forbidden", http.StatusForbidden)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "request too large", http.StatusBadRequest)
		return
	}
	body, err := validateRequest(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.forward(w, r, data, body)
}

func (g *gate) reserve(ctx context.Context, data []byte, body requestBody) (record, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return record{}, err
	}
	rec := record{ID: hex.EncodeToString(id[:]), Model: selectedModel, Status: "reserved"}
	err := updateLedger(ctx, g.path, func(l *ledger) error {
		// A token cannot exceed its serialized UTF-8 bytes; allowance covers protocol framing.
		input := int64(len(data)) + protocolAllowance
		rec.Reservation = cost(input, max(l.InputRate, l.CachedRate, l.CacheWriteRate)) + cost(body.MaxOutput, l.OutputRate)
		if rec.Reservation > l.Ceiling-l.Spent-l.Reserved {
			return errors.New("development ceiling reached")
		}
		l.Reserved += rec.Reservation
		return l.addRecord(rec)
	})
	return rec, err
}
func cost(tokens, rate int64) int64 { return (tokens*rate + 999999) / 1000000 }

func (g *gate) forward(w http.ResponseWriter, r *http.Request, data []byte, body requestBody) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.upstream+"/v1/responses", bytes.NewReader(data))
	if err != nil {
		http.Error(w, "invalid upstream configuration", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+g.key)
	req.Header.Set("Content-Type", "application/json")
	rec, err := g.reserve(ctx, data, body)
	if err != nil {
		http.Error(w, "budget unavailable or ceiling reached", http.StatusForbidden)
		return
	}
	res, err := g.client.Do(req)
	if err != nil {
		http.Error(w, "upstream unavailable; reservation retained", http.StatusFailedDependency)
		return
	}
	defer res.Body.Close() //nolint:errcheck // Close failure cannot change dispatched accounting; uncertain usage keeps the reservation.
	if res.StatusCode != http.StatusOK {
		http.Error(w, "upstream rejected request; reservation retained", http.StatusFailedDependency)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	usage, err := relay(w, io.LimitReader(res.Body, 16*1024*1024))
	if err != nil || usage == nil {
		return
	}
	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer settleCancel()
	// Failure leaves the durable reservation intact; no refund is inferred.
	if err := g.settle(settleCtx, rec, *usage); err != nil {
		fmt.Fprintln(os.Stderr, "live gate settlement unavailable; reservation retained")
	}
}

type (
	terminalUsage struct {
		ID                    string
		Input, Cached, Output int64
	}
	terminalEvent struct {
		Type     string `json:"type"`
		Response struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage *struct {
				Input   *int64 `json:"input_tokens"`
				Output  *int64 `json:"output_tokens"`
				Details struct {
					Cached int64 `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
)

func parseUsage(data []byte) *terminalUsage {
	var e terminalEvent
	if json.Unmarshal(data, &e) != nil || e.Type != "response.completed" || e.Response.Model != selectedModel || e.Response.ID == "" {
		return nil
	}
	u := e.Response.Usage
	if u == nil || u.Input == nil || u.Output == nil {
		return nil
	}
	result := &terminalUsage{ID: e.Response.ID, Input: *u.Input, Cached: u.Details.Cached, Output: *u.Output}
	if !result.valid() {
		return nil
	}
	return result
}

func relay(w http.ResponseWriter, reader io.Reader) (*terminalUsage, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var usage *terminalUsage
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			if u := parseUsage([]byte(strings.TrimPrefix(line, "data: "))); u != nil {
				if usage != nil {
					return nil, errors.New("duplicate terminal usage")
				}
				usage = u
			}
		}
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return nil, err
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	return usage, scanner.Err()
}

func (g *gate) settle(ctx context.Context, rec record, u terminalUsage) error {
	return updateLedger(ctx, g.path, func(l *ledger) error {
		for _, raw := range l.Records {
			var previous record
			if json.Unmarshal(raw, &previous) == nil && previous.ID == rec.ID && previous.Status == "settled" {
				return errors.New("attempt already settled")
			}
		}
		rec.Status = "settled"
		rec.RequestID = u.ID
		rec.Input = u.Input
		rec.Cached = u.Cached
		rec.Output = u.Output
		rec.Cost = cost(u.Input-u.Cached, l.InputRate) + cost(u.Cached, l.CachedRate) + cost(u.Output, l.OutputRate)
		if rec.Cost > rec.Reservation || l.Reserved < rec.Reservation {
			return errors.New("usage exceeds reservation")
		}
		l.Reserved -= rec.Reservation
		l.Spent += rec.Cost
		return l.addRecord(rec)
	})
}

func (u terminalUsage) valid() bool {
	return u.Input >= 0 && u.Output >= 0 && u.Cached >= 0 && u.Cached <= u.Input && u.Input <= maxBody+protocolAllowance && u.Output <= 8192
}
