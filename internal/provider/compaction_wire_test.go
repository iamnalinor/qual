package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestResponsesCompactionWireHeadroom(t *testing.T) {
	request := domain.ModelRequest{RunID: "run", AgentID: "reviewer", Model: "fixture", MaxOutputTokens: 8192, Messages: []domain.Message{{Role: "system", Content: strings.Repeat("Review contract. ", 300)}, {Role: "user", Content: strings.Repeat("Fixed snapshot metadata. ", 200)}}}
	for i := range 14 {
		request.Tools = append(request.Tools, domain.ToolDefinition{Name: fmt.Sprintf("tool_%d", i), Description: strings.Repeat("Inspect relevant evidence. ", 4), InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["path"],"additionalProperties":false}`)})
	}
	for i := range 4 {
		request.Messages = append(request.Messages, readWireMessages(t, i, 12000)...)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	// Fill the normalized request just below the runtime's 96 KiB threshold.
	padding := 96*1024 - 1 - len(data)
	if padding < 0 {
		t.Fatal("fixture exceeds compaction threshold")
	}
	request.Messages[1].Content += strings.Repeat("x", padding)
	assertCompactionWireBound(t, request)
	// One maximum-sized code read may cross the threshold. Compaction removes
	// tool schemas and adds its summary instruction before the actual encoding.
	request.Messages = append(request.Messages, readWireMessages(t, 4, 32*1024)...)
	request.Messages = append(request.Messages, domain.Message{Role: "user", Content: "Summarize context for continuation, preserving findings and evidence IDs. Do not invoke tools."})
	request.Tools = nil
	assertCompactionWireBound(t, request)
}

func readWireMessages(t *testing.T, index, size int) []domain.Message {
	t.Helper()
	id := fmt.Sprintf("read-%d", index)
	code := strings.Repeat("if result == nil { return fmt.Errorf(\"missing result for requested repository\") }\n", size/80+1)
	output, err := json.Marshal(domain.ToolResult{CallID: id, Content: code[:size]})
	if err != nil {
		t.Fatal(err)
	}
	return []domain.Message{{Role: "assistant", ToolCalls: []domain.ToolCall{{ID: id, Name: "read_file", Arguments: json.RawMessage(`{"path":"code.go","offset":0,"limit":32768}`)}}}, {Role: "tool", ToolCallID: id, Content: string(output)}}
}

func assertCompactionWireBound(t *testing.T, request domain.ModelRequest) {
	t.Helper()
	_, body, err := responsesRequest(request, false)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual Responses body: %d bytes", len(wire))
	if len(wire) > 128*1024 {
		t.Fatalf("actual Responses body %d exceeds development gate's 128 KiB bound", len(wire))
	}
}
