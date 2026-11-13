package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutableDoctorHumanDefaultAndStructuredOptIn(t *testing.T) {
	binary := buildCLI(t)
	home := filepath.Join(t.TempDir(), "absent-home")
	cwd := t.TempDir()
	out, diagnostic, err := invoke(t, binary, home, cwd, "", "doctor")
	if err != nil {
		t.Fatalf("doctor %v %s", err, diagnostic)
	}
	if json.Valid([]byte(out)) || !strings.Contains(out, "Qual doctor") || !strings.Contains(out, "System") || !strings.Contains(out, "Sandbox") || !strings.Contains(out, "not active") || strings.Contains(out, "\x1b") {
		t.Fatalf("unhelpful human doctor: %s", out)
	}
	if _, err = os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("doctor unnecessarily created home: %v", err)
	}
	out, diagnostic, err = invoke(t, binary, home, cwd, "", "doctor", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("structured doctor %v %s %s", err, out, diagnostic)
	}
	verifyDoctorJSON(t, out)
}

func TestExecutableDoctorFindsTrustedAdjacentLauncher(t *testing.T) {
	layout := t.TempDir()
	binary := filepath.Join(layout, "bin", "qual-preview")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- Compile a fixture binary into the documented trusted installation layout.
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/qual")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s %v", out, err)
	}
	scripts := filepath.Join(layout, "scripts")
	if err := os.Mkdir(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	// #nosec G306 -- Private fixture launcher requires an executable owner bit for metadata discovery; it is never executed.
	if err := os.WriteFile(filepath.Join(scripts, "qual-sandbox"), []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, err := invoke(t, binary, t.TempDir(), t.TempDir(), "", "doctor")
	if err != nil || !strings.Contains(out, "available beside executable") || !strings.Contains(out, filepath.Join(scripts, "qual-sandbox")) {
		t.Fatalf("adjacent launcher missing: %v %s %s", err, out, diagnostic)
	}
}

func verifyDoctorJSON(t *testing.T, out string) {
	t.Helper()
	var report struct {
		OS, Arch string
		Sandbox  struct {
			ReportedActive bool `json:"reported_active"`
		}
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.OS == "" || report.Arch == "" || report.Sandbox.ReportedActive {
		t.Fatalf("invalid doctor distinction: %s %v", out, err)
	}
}

func TestExecutableServiceCommandsDefaultHumanJSONOptIn(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	configure(t, home, "http://127.0.0.1:1/v1")
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	review, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--staged", "--json", "--non-interactive")
	if err != nil {
		t.Fatalf("no changes %v %s", err, diagnostic)
	}
	var state struct {
		RunID string `json:"run_id"`
	}
	if err = json.Unmarshal([]byte(review), &state); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args     []string
		contains string
	}{
		{[]string{"models", "list"}, "gpt-6-luna"},
		{[]string{"config", "show"}, "Configuration"},
		{[]string{"config", "validate"}, "valid"},
		{[]string{"sessions", "list"}, state.RunID},
		{[]string{"sessions", "show", state.RunID}, "no_changes"},
		{[]string{"cleanup"}, "worktrees"},
		{[]string{"auth", "list"}, "profiles"},
		{[]string{"mcp", "list"}, "MCP"},
		{[]string{"ci", "generate", "github", "--output", t.TempDir()}, "SETUP.md"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "-"), func(t *testing.T) {
			out, diagnostic, err := invoke(t, binary, home, repo, "", tc.args...)
			if err != nil || json.Valid([]byte(out)) || !strings.Contains(out, tc.contains) || strings.Contains(out, "\x1b") {
				t.Fatalf("human %v: %v %s %s", tc.args, err, out, diagnostic)
			}
			args := append(append([]string(nil), tc.args...), "--json")
			out, diagnostic, err = invoke(t, binary, home, repo, "", args...)
			if err != nil || !json.Valid([]byte(out)) {
				t.Fatalf("JSON %v: %v %s %s", args, err, out, diagnostic)
			}
		})
	}
}

func TestExecutableMissingSavedSignInHasActionableGuidance(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	configure(t, home, "http://127.0.0.1:1/v1")
	path := filepath.Join(home, "settings.yaml")
	// #nosec G304 -- Replace a test-owned credential reference with a deliberately absent saved profile.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "env:QUAL_FIXTURE_API_KEY", "profile:absent"))
	// #nosec G703 -- Path is the unchanged settings.yaml under t.TempDir; fixture content never selects the destination.
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, diagnostic, err := invoke(t, binary, home, t.TempDir(), "", "models", "list", "--json")
	if err == nil || !strings.Contains(diagnostic, "qual init") || !strings.Contains(diagnostic, "qual auth list") {
		t.Fatalf("missing sign-in has no next step: %v %s", err, diagnostic)
	}
	diagnosticJSONLines(t, diagnostic)
}

func TestExecutableHumanErrorsLeadWithProblemAndKeepJSONCodes(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	_, diagnostic, err := invoke(t, binary, home, t.TempDir(), "", "models", "list")
	if err == nil || strings.HasPrefix(diagnostic, "invalid_config:") || !strings.Contains(diagnostic, "No provider is configured") || !strings.Contains(diagnostic, "qual init") {
		t.Fatalf("unreadable problem: %v %s", err, diagnostic)
	}
	_, structured, err := invoke(t, binary, home, t.TempDir(), "", "models", "list", "--json")
	if err == nil {
		t.Fatal("missing provider accepted")
	}
	var event struct {
		Code string `json:"code"`
	}
	if err = json.Unmarshal([]byte(structured), &event); err != nil || event.Code != "invalid_config" {
		t.Fatalf("JSON code changed: %v %s", err, structured)
	}
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	_, diagnostic, err = invoke(t, binary, home, t.TempDir(), "", "init", "--connection", "primary", "--kind", "openai", "--model", "gpt-6-luna", "--credential", "env:QUAL_FIXTURE_API_KEY", "--endpoint", "http://127.0.0.1:1/v1", "--non-interactive")
	if err == nil || !strings.Contains(diagnostic, "connect: connection refused") || strings.HasPrefix(diagnostic, "auth:") {
		t.Fatalf("cause lost: %v %s", err, diagnostic)
	}
}
