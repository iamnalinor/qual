package cli_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExecutableMCPProbesNeverInvokeTools(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	cwd := t.TempDir()
	var calls atomic.Int32
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	for _, name := range []string{"read", "write"} {
		server.AddTool(&sdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls.Add(1)
			return &sdk.CallToolResult{}, nil
		})
	}
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	defer httpServer.Close()
	settings := "schema_version: 1\nmcp_servers:\n  fixture:\n    transport: http\n    url: " + httpServer.URL + "\n    policy: read-only\n    trusted: true\n    read_only_tools: [read]\nmcp: [fixture]\n"
	if err := os.WriteFile(filepath.Join(home, "settings.yaml"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"list", "test"} {
		out, diagnostic, err := invoke(t, binary, home, cwd, "", "mcp", name, "--json")
		if err != nil || !strings.Contains(out, "read") || strings.Contains(out, `"write"`) {
			t.Fatalf("probe: %v %s %s", err, out, diagnostic)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("probe invoked %d tools", calls.Load())
	}
}
