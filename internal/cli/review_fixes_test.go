package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
	"gopkg.in/yaml.v3"
)

func diagnosticJSONLines(t *testing.T, text string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("non-JSON diagnostic line %q", line)
		}
	}
}

func TestExecutableStructuredProposalsAndVisibleHistory(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	server, _ := fixture(t)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	configure(t, home, server.URL+"/v1")
	out, diagnostic, err := invoke(t, binary, home, repo, "", "setup", "--trust", "untrusted", "--accept", "--non-interactive", "--json")
	if err != nil {
		t.Fatalf("setup: %v %s %s", err, out, diagnostic)
	}
	diagnosticJSONLines(t, diagnostic)
	if !strings.Contains(diagnostic, `"type":"proposal"`) || !strings.Contains(diagnostic, `"type":"acceptance"`) {
		t.Fatalf("missing proposal/acceptance %s", diagnostic)
	}
	var state domain.RunState
	if err = json.Unmarshal([]byte(out), &state); err != nil {
		t.Fatal(err)
	}
	shown, diagnostic, err := invoke(t, binary, home, repo, "", "sessions", "show", string(state.RunID), "--json")
	if err != nil {
		t.Fatalf("show: %v %s", err, diagnostic)
	}
	for _, field := range []string{`"history"`, `"tool_intent"`, `"tool_result"`, `"write_file"`, `"artifact_id"`, `"model":"gpt-6-luna"`, `"spent"`, `"soft"`} {
		if !strings.Contains(shown, field) {
			t.Fatalf("history missing %s: %s", field, shown)
		}
	}
	for _, private := range []string{`"provider_state"`, `"provider_data"`, `"messages"`, `"arguments"`, "fixture-only-credential"} {
		if strings.Contains(shown, private) {
			t.Fatalf("history exposed %s", private)
		}
	}
}

func TestExecutableInitJSONLines(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	server, _ := fixture(t)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	out, diagnostic, err := invoke(t, binary, home, t.TempDir(), "yes\n", "init", "--model", "gpt-6-luna", "--endpoint", server.URL+"/v1", "--credential", "env:QUAL_FIXTURE_API_KEY", "--json")
	if err != nil {
		t.Fatalf("init: %v %s %s", err, out, diagnostic)
	}
	diagnosticJSONLines(t, diagnostic)
}

func TestExecutableUILanguageSeparateFromReview(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	server, _ := fixture(t)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	configure(t, home, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed language fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--ui-language", "ru", "--review-language", "fr", "--json")
	if err != nil {
		t.Fatalf("review: %v %s %s", err, out, diagnostic)
	}
	if !strings.Contains(diagnostic, "Внешняя изоляция") || !strings.Contains(diagnostic, "Действие") {
		t.Fatalf("Russian UI not rendered: %s", diagnostic)
	}
	var state domain.RunState
	if err = json.Unmarshal([]byte(out), &state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Results[0].Report, "Rapport français") {
		t.Fatalf("report language lost: %s", out)
	}
	verifyLanguageSpecs(t, state)
	_, diagnostic, err = invoke(t, binary, home, repo, "", "review", "--ui-language", "unsupported-ui", "--json", "--non-interactive")
	if err == nil || !strings.Contains(diagnostic, "unsupported_capability") {
		t.Fatalf("unsupported UI silently accepted: %v %s", err, diagnostic)
	}
}

func TestExecutableJSONErrorsRemainJSONLines(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	_, diagnostic, err := invoke(t, binary, home, repo, "", "models", "list", "--json")
	if err == nil {
		t.Fatal("missing configured connection succeeded")
	}
	if !strings.Contains(diagnostic, "qual init") || !strings.Contains(diagnostic, "account") {
		t.Fatalf("missing provider has no next step: %s", diagnostic)
	}
	diagnosticJSONLines(t, diagnostic)
	configure(t, home, "http://127.0.0.1:1/v1")
	if err = os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed missing credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, diagnostic, err = invoke(t, binary, home, repo, "", "review", "--json", "--non-interactive")
	if err == nil {
		t.Fatal("missing credential succeeded")
	}
	diagnosticJSONLines(t, diagnostic)
}

func verifyLanguageSpecs(t *testing.T, state domain.RunState) {
	t.Helper()
	for _, agent := range state.Agents {
		if agent.Language != "fr" || !strings.Contains(agent.Instructions, "Ask user questions in Russian (ru)") {
			t.Fatalf("languages coupled: %+v", agent)
		}
	}
}

func TestExecutableBudgetNoticesAreSafeProgress(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	server, _ := fixture(t)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	configure(t, home, server.URL+"/v1")
	budgetSettings(t, home)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed budget progress\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--json", "--non-interactive")
	if err != nil {
		t.Fatalf("budget review: %v %s %s", err, out, diagnostic)
	}
	diagnosticJSONLines(t, diagnostic)
	for _, field := range []string{`"type":"budget_threshold"`, `"type":"budget_phase"`, `"model":"gpt-6-luna"`, `"tool":"read_file"`, `"spent"`, `"soft"`, `"threshold_percent"`} {
		if !strings.Contains(diagnostic, field) {
			t.Fatalf("missing safe progress %s: %s", field, diagnostic)
		}
	}
	if strings.Contains(diagnostic, `"arguments"`) || strings.Contains(diagnostic, "changed budget progress") {
		t.Fatalf("raw tool payload in progress: %s", diagnostic)
	}
}

func budgetSettings(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, "settings.yaml")
	// #nosec G304 -- Fixture-owned user configuration receives explicit test budgets.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings config.Settings
	if err = yaml.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	for i := range settings.Reviewers {
		settings.Reviewers[i].Soft = "0.00005"
		settings.Reviewers[i].Hard = "0.01"
	}
	settings.Leader.Soft = "0.00005"
	settings.Leader.Hard = "0.01"
	data, err = yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
