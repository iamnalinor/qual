package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestHistoryProjectsActionsAndBudgetsWithoutPrivatePayloads(t *testing.T) {
	events := []domain.Event{
		{RunID: "run", AgentID: "reviewer", Type: "agent_checkpoint", Data: json.RawMessage(`{"spec":{"model":"gpt-6-luna","role":"reviewer"},"phase":"investigation","ledger":{"spent":17,"reserved":5,"cost_known":false,"limits":{"soft":100,"hard":200}},"messages":[{"provider_data":{"private":"hidden-continuation"}}],"provider_state":{"private":"hidden-continuation"}}`)},
		{RunID: "run", AgentID: "reviewer", Type: "tool_intent", Data: json.RawMessage(`{"id":"call","name":"shell","arguments":{"command":"credential-value"}}`)},
		{RunID: "run", AgentID: "reviewer", Type: "tool_result", Data: json.RawMessage(`{"call":{"id":"call","name":"shell"},"result":{"content":"credential-value","is_error":true,"artifact_id":"digest"}}`)},
	}
	history, err := visibleHistory(events)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"credential-value", "hidden-continuation", "messages", "arguments", "provider_state", "provider_data"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("private payload exposed: %s", data)
		}
	}
	assertHistoryCall(t, history[len(history)-1])
}

func assertHistoryCall(t *testing.T, last visibleEvent) {
	t.Helper()
	if last.Tool != "shell" || last.CallID != "call" || !last.IsError || last.ArtifactID != "digest" || last.Model != "gpt-6-luna" || last.Budget.Spent != 17 || last.Budget.Soft != 100 {
		t.Fatalf("useful execution detail missing: %+v", last)
	}
}

func TestToolTimelineShowsOnlyBoundedRelativeFileTargets(t *testing.T) {
	events := []domain.Event{
		{Type: "tool_intent", Data: json.RawMessage(`{"id":"read","name":"read_file","arguments":{"path":"src/main.go","secret":"private"}}`)},
		{Type: "tool_intent", Data: json.RawMessage(`{"id":"shell","name":"shell","arguments":{"command":"private"}}`)},
		{Type: "tool_intent", Data: json.RawMessage(`{"id":"outside","name":"read_file","arguments":{"path":"../../outside"}}`)},
	}
	history, err := visibleHistory(events)
	if err != nil {
		t.Fatal(err)
	}
	if history[0].Target != "src/main.go" || history[1].Target != "" || history[2].Target != "" {
		t.Fatalf("file targets: %+v", history)
	}
}
