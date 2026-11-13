package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureLedger(t *testing.T, ceiling int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budget.json")
	// Independent disposable receipt: tests never read or reset the real development ledger.
	l := ledger{Schema: 1, Currency: "USD", Ceiling: ceiling, Model: selectedModel, CheckedAt: "2026-11-13", Source: "https://developers.openai.com/api/docs/models/gpt-6-luna", InputRate: 100000000, CachedRate: 10000000, CacheWriteRate: 125000000, OutputRate: 500000000, Records: []json.RawMessage{json.RawMessage(`{"operation":"models.list","model_available":true,"inference_requests":0,"cost_nanodollars":0}`)}}
	data, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadLedger(t *testing.T, path string) ledger {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // Test fixture path.
	if err != nil {
		t.Fatal(err)
	}
	var l ledger
	if err = json.Unmarshal(data, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func gateRequest(t *testing.T, url, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := res.Body.Close(); e != nil {
			t.Error(e)
		}
	}()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(data)
}

const validBody = `{"model":"gpt-6-luna","input":"review source","stream":true,"store":false,"max_output_tokens":64}`

func TestGateSettlesUsageAndRoutesCredentials(t *testing.T) {
	path := fixtureLedger(t, 10000000000)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer real-secret" || r.URL.Path != "/v1/responses" {
			t.Error("wrong credential or endpoint")
		}
		l := loadLedger(t, path)
		if l.Reserved == 0 || len(l.Records) != 2 {
			t.Error("dispatch preceded durable reservation")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"model\":\"gpt-6-luna\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":40}}}}\n\n")
		if err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	srv := httptest.NewServer(newGate(path, "limited-token", "real-secret", upstream.URL, upstream.Client()))
	defer srv.Close()
	code, body := gateRequest(t, srv.URL, "limited-token", validBody)
	if code != 200 || !strings.Contains(body, "resp_test") {
		t.Fatalf("response %d %s", code, body)
	}
	l := loadLedger(t, path)
	if l.Spent != 16400 || l.Reserved != 0 {
		t.Fatalf("incorrect settlement: %+v", l)
	}
	data, err := os.ReadFile(path) //nolint:gosec // Test fixture path.
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, string(data))
}

func assertNoSecrets(t *testing.T, data string) {
	t.Helper()
	for _, secret := range []string{"real-secret", "limited-token", "review source"} {
		if strings.Contains(data, secret) {
			t.Fatal("sensitive data persisted")
		}
	}
}

func TestGateRejectsBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	path := fixtureLedger(t, 10000000000)
	srv := httptest.NewServer(newGate(path, "token", "secret", upstream.URL, upstream.Client()))
	defer srv.Close()
	for _, body := range []string{
		strings.Replace(validBody, "gpt-6-luna", "other", 1), strings.Replace(validBody, "64", "8193", 1),
		strings.Replace(validBody, `"store":false`, `"store":true`, 1), strings.Replace(validBody, `"stream":true`, `"stream":false`, 1),
		strings.TrimSuffix(validBody, "}") + `,"tools":[{"type":"web_search"}]}`,
		strings.TrimSuffix(validBody, "}") + `,"service_tier":"priority"}`, `{}`, "{", strings.Repeat("x", maxBody+1),
	} {
		if code, _ := gateRequest(t, srv.URL, "token", body); code < 400 {
			t.Fatalf("accepted %s", body[:min(len(body), 200)])
		}
	}
	if code, _ := gateRequest(t, srv.URL, "wrong", validBody); code != 401 {
		t.Fatal(code)
	}
	if calls.Load() != 0 {
		t.Fatal("rejected input dispatched")
	}
}

func TestGateUnknownUsageRetainsReservation(t *testing.T) {
	path := fixtureLedger(t, 10000000000)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	srv := httptest.NewServer(newGate(path, "token", "secret", upstream.URL, upstream.Client()))
	defer srv.Close()
	gateRequest(t, srv.URL, "token", validBody)
	l := loadLedger(t, path)
	if l.Reserved == 0 || l.Spent != 0 {
		t.Fatalf("unknown usage refunded: %+v", l)
	}
	// A new gate/process reads the same unresolved reservation.
	if err := updateLedger(context.Background(), path, func(l *ledger) error {
		if l.Reserved == 0 {
			return fmt.Errorf("lost reserve")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerConcurrentCeilingAndMalformedState(t *testing.T) {
	path := fixtureLedger(t, 100)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := updateLedger(t.Context(), path, func(l *ledger) error {
				if l.Spent+l.Reserved+30 > l.Ceiling {
					return fmt.Errorf("ceiling")
				}
				l.Reserved += 30
				return l.addRecord(record{ID: fmt.Sprintf("thread-%d", accepted.Load()), Model: selectedModel, Status: "reserved", Reservation: 30})
			}); err == nil {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 3 || loadLedger(t, path).Reserved != 90 {
		t.Fatal("lost atomic reservation")
	}
	for _, data := range []string{`{}`, `{"schema_version":1,"ceiling_nanodollars":-1}`, `not-json`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := updateLedger(t.Context(), path, func(*ledger) error { return nil }); err == nil {
			t.Fatal("accepted malformed ledger")
		}
	}
	if err := updateLedger(t.Context(), filepath.Join(t.TempDir(), "missing"), func(*ledger) error { return nil }); err == nil {
		t.Fatal("initialized missing ledger")
	}
}

func TestGateCeilingRejectsWithoutDispatch(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	srv := httptest.NewServer(newGate(fixtureLedger(t, 1), "token", "secret", upstream.URL, upstream.Client()))
	defer srv.Close()
	if code, _ := gateRequest(t, srv.URL, "token", validBody); code != http.StatusForbidden {
		t.Fatal(code)
	}
	if calls.Load() != 0 {
		t.Fatal("over-ceiling request left gate")
	}
}

func TestGateCancellationKeepsDurableReserve(t *testing.T) {
	path := fixtureLedger(t, 10000000000)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	srv := httptest.NewServer(newGate(path, "token", "secret", upstream.URL, upstream.Client()))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(validBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token")
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, e := http.DefaultClient.Do(req)
		if e == nil {
			if e = res.Body.Close(); e != nil {
				t.Error(e)
			}
		}
	}()
	<-started
	cancel()
	<-done
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream not cancelled")
	}
	if loadLedger(t, path).Reserved == 0 {
		t.Fatal("cancelled dispatched request refunded")
	}
}

func TestGateForbidsExternalInputAndPaths(t *testing.T) {
	for _, extra := range []string{`,"previous_response_id":"resp_old"`, `,"conversation":"conv_id"`, `,"background":true`, `,"prompt":{"id":"prompt"}`} {
		if _, err := validateRequest([]byte(strings.TrimSuffix(validBody, "}") + extra + "}")); err == nil {
			t.Fatal("accepted unbounded request", extra)
		}
	}
	for _, input := range []string{`[{"type":"item_reference","id":"x"}]`, `[{"role":"user","content":[{"type":"input_file","file_id":"x"}]}]`} {
		body := strings.Replace(validBody, `"review source"`, input, 1)
		if _, err := validateRequest([]byte(body)); err == nil {
			t.Fatal("accepted external input")
		}
	}
	g := newGate(fixtureLedger(t, 10000000000), "token", "secret", "", http.DefaultClient)
	for _, path := range []string{"/v1/models", "/v1/responses?other=1", "/v1/chat/completions"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(validBody))
		req.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatal("accepted path", path)
		}
	}
}

func TestLedgerProcessHelper(t *testing.T) {
	path := os.Getenv("QUAL_GATE_LEDGER_HELPER")
	if path == "" {
		return
	}
	err := updateLedger(t.Context(), path, func(l *ledger) error {
		if l.Reserved+30 > l.Ceiling-l.Spent {
			return fmt.Errorf("ceiling")
		}
		l.Reserved += 30
		return l.addRecord(record{ID: fmt.Sprintf("process-%d", os.Getpid()), Model: selectedModel, Status: "reserved", Reservation: 30})
	})
	if err != nil {
		os.Exit(20)
	}
	// Exit without orderly process cleanup: flock releases but balance persists.
	os.Exit(21)
}

func TestLedgerCrossProcessCrashRetention(t *testing.T) {
	path := fixtureLedger(t, 100)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 8 {
		wg.Go(func() {
			cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestLedgerProcessHelper$")
			cmd.Env = append(os.Environ(), "QUAL_GATE_LEDGER_HELPER="+path)
			err := cmd.Run()
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 21 {
				successes.Add(1)
				return
			}
			if !errors.As(err, &exit) || exit.ExitCode() != 20 {
				t.Errorf("unexpected helper result: %v", err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 3 || loadLedger(t, path).Reserved != 90 {
		t.Fatal("cross-process crash lost reservation")
	}
}

func TestGateDoesNotFollowCredentialRedirects(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { leaked.Store(r.Header.Get("Authorization") != "") }))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	srv := httptest.NewServer(newGate(fixtureLedger(t, 10000000000), "token", "secret", upstream.URL, upstream.Client()))
	defer srv.Close()
	if code, _ := gateRequest(t, srv.URL, "token", validBody); code != http.StatusFailedDependency {
		t.Fatal(code)
	}
	if leaked.Load() {
		t.Fatal("upstream credential followed redirect")
	}
}

func TestGateAcceptsRealInlineFunctionRequest(t *testing.T) {
	body := `{"model":"gpt-6-luna","input":[{"role":"user","content":"inspect source"}],"stream":true,"store":false,"max_output_tokens":128,"tools":[{"type":"function","name":"read_file","description":"Read source","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}],"reasoning":{"effort":"low"},"include":["reasoning.encrypted_content"]}`
	if _, err := validateRequest([]byte(body)); err != nil {
		t.Fatalf("ordinary Qual function request rejected: %v", err)
	}
}

func TestFunctionOutputMustRemainInlineString(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	path := fixtureLedger(t, 10000000000)
	srv := httptest.NewServer(newGate(path, "token", "secret", upstream.URL, upstream.Client()))
	defer srv.Close()
	for _, output := range []string{`[{"type":"input_image","image_url":"https://example.test/large.png"}]`, `[{"type":"input_file","file_id":"file_unbounded"}]`, `null`, `{"type":"input_file","file_id":"file_unbounded"}`} {
		input := `[{"type":"function_call_output","call_id":"read-source","output":` + output + `}]`
		body := strings.Replace(validBody, `"review source"`, input, 1)
		if code, _ := gateRequest(t, srv.URL, "token", body); code != http.StatusBadRequest {
			t.Fatalf("nonstring function output accepted: %d", code)
		}
	}
	if calls.Load() != 0 || loadLedger(t, path).Reserved != 0 {
		t.Fatal("nonstring output reserved or dispatched")
	}
	input := `[{"type":"function_call_output","call_id":"read-source","output":"inline source contents"}]`
	if code, _ := gateRequest(t, srv.URL, "token", strings.Replace(validBody, `"review source"`, input, 1)); code != http.StatusOK {
		t.Fatal("valid string rejected", code)
	}
	if calls.Load() != 1 {
		t.Fatal("valid string not dispatched exactly once")
	}
}
