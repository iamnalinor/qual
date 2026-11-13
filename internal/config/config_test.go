package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestStrictLayering(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "settings.yaml"), "connections:\n  main:\n    kind: openai\n    credential: env:QUAL_TEST_KEY\nreviewers:\n  - id: first\n    provider: main\n    model: small\nleader:\n  id: lead\n  provider: main\n  model: small\n")
	e, err := Resolve(context.Background(), Request{Home: home, CLI: []byte("reviewers:\n  - id: second\n    provider: main\n    model: big\nconnections:\n  main:\n    endpoint: https://example.invalid/v1\n")})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Agents) != 2 || e.Agents[0].ID != "second" || e.Settings.Connections["main"].Kind != "openai" {
		t.Fatalf("bad merge: %+v", e)
	}
	if e.Settings.Trust != "untrusted" || e.Provenance["connections.main.kind"].Source != "user" {
		t.Fatal("missing trust/provenance")
	}
	for _, input := range []string{"unknown: true", "trust: trusted\ntrust: untrusted", "connections:\n  x:\n    kind: openai\n    credential: plaintext-secret"} {
		_, err = Resolve(context.Background(), Request{Home: t.TempDir(), CLI: []byte(input)})
		if err == nil || strings.Contains(err.Error(), "plaintext-secret") {
			t.Fatalf("unsafe validation for %q: %v", input, err)
		}
	}
}

func TestRepositoryPrivilegeRejection(t *testing.T) {
	for _, input := range []string{"trust: trusted", "connections: {}", "mcp_servers: {}", "leader:\n  hard: '1'"} {
		repo, rev := repository(t, map[string]string{".qual/settings.yaml": input})
		_, err := Resolve(context.Background(), Request{Home: t.TempDir(), Snapshot: domain.Snapshot{StorePath: repo, RulesRevision: rev, ProjectID: "project"}})
		if err == nil {
			t.Fatalf("accepted repository privilege: %s", input)
		}
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMCPRequiresExplicitToolGrants(t *testing.T) {
	for _, input := range []string{
		"mcp_servers:\n  x:\n    transport: stdio\n    command: [server]\n    policy: full\n    full_tools: [write]\n",
		"mcp_servers:\n  x:\n    transport: stdio\n    command: [server]\n    policy: read-only\n    full_tools: [write]\n    trusted: true\n",
	} {
		if _, err := Resolve(context.Background(), Request{Home: t.TempDir(), CLI: []byte(input)}); err == nil {
			t.Fatal("accepted unauthorized MCP grant")
		}
	}
	input := "connections:\n  compat:\n    kind: openai-compatible\n  legacy:\n    kind: codex\nmcp_servers:\n  x:\n    transport: stdio\n    command: [server]\n    policy: read-only\n    read_only_tools: [read]\n    trusted: true\nmcp: [x]\n"
	e, err := Resolve(context.Background(), Request{Home: t.TempDir(), CLI: []byte(input)})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Settings.MCPServers["x"].ReadOnlyTools) != 1 {
		t.Fatal("lost tool grants")
	}
}

func TestBudgetAndUnknownFieldsFailClosed(t *testing.T) {
	base := "connections:\n  main:\n    kind: openai\nleader:\n  id: lead\n  provider: main\n  model: small\n"
	for _, suffix := range []string{"  soft: '2'\n  hard: '1'", "  hard: '0.0000000001'", "  reasoning: invented", "  api_key: hidden", "  model: duplicate"} {
		if _, err := Resolve(context.Background(), Request{Home: t.TempDir(), CLI: []byte(base + suffix)}); err == nil {
			t.Fatalf("accepted invalid settings: %s", suffix)
		}
	}
	e, err := Resolve(context.Background(), Request{Home: t.TempDir(), CLI: []byte(base + "  soft: '0.000000001'\n  hard: '1'\n")})
	if err != nil {
		t.Fatal(err)
	}
	if e.Agents[0].Budget.Soft != 1 || e.Agents[0].Budget.Hard != 1000000000 {
		t.Fatal("budget was rounded")
	}
}

func TestExplicitEnvironmentAndReferenceOnlyArtifacts(t *testing.T) {
	t.Setenv("QUAL_REVIEW_LANGUAGE", "ignored")
	input := []byte("connections:\n  main:\n    kind: openai\n    credential: env:QUAL_REFERENCE_ONLY\n")
	t.Setenv("QUAL_REFERENCE_ONLY", "not-for-artifacts")
	e, err := Resolve(context.Background(), Request{Home: t.TempDir(), CLI: input, Environment: map[string]string{"QUAL_UI_LANGUAGE": "fr"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Redacted()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "not-for-artifacts") || e.Settings.ReviewLanguage != "" || e.Settings.UILanguage != "fr" {
		t.Fatal("implicit environment or credential resolution")
	}
	if _, err = Resolve(context.Background(), Request{Home: t.TempDir(), Environment: map[string]string{"QUAL_TRUST": "trusted"}}); err == nil {
		t.Fatal("accepted unsupported environment authority")
	}
}

func TestInvestigationModesAreIndependentOfReasoningEffort(t *testing.T) {
	base := "connections:\n  main:\n    kind: openai\nleader:\n  id: lead\n  provider: main\n  model: small\n  reasoning: high\n"
	for _, mode := range []string{"", "light", "full"} {
		t.Run(mode, func(t *testing.T) {
			input := base
			if mode != "" {
				input += "  investigation: " + mode + "\n"
			}
			e, err := Resolve(context.Background(), Request{Home: t.TempDir(), CLI: []byte(input)})
			if err != nil {
				t.Fatal(err)
			}
			if e.Agents[0].ReasoningEffort != "high" || e.Agents[0].InvestigationEffort != mode {
				t.Fatalf("effort settings conflated: %+v", e.Agents[0])
			}
		})
	}
	if _, err := Resolve(context.Background(), Request{Home: t.TempDir(), CLI: []byte(base + "  investigation: high\n")}); err == nil {
		t.Fatal("reasoning level accepted as investigation mode")
	}
	if _, err := Resolve(context.Background(), Request{Home: t.TempDir(), CLI: []byte(strings.ReplaceAll(base, "reasoning: high", "reasoning: full") + "  investigation: light\n")}); err == nil {
		t.Fatal("investigation mode accepted as reasoning level")
	}
}
