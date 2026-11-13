package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/config"
)

func TestOnlySavedProviderIsSelectedWithoutInternalName(t *testing.T) {
	a := &App{}
	e := config.Effective{Settings: config.Settings{Connections: map[string]config.Connection{"personal": {Kind: "openai", Credential: "env:KEY"}}}}
	p, err := a.selected(e, "")
	if err != nil || p.connection.Kind != "openai" {
		t.Fatalf("sole provider selection: %v %v", p, err)
	}
}

func TestFreshWelcomeHasOneClearStartingAction(t *testing.T) {
	t.Setenv("QUAL_HOME", t.TempDir())
	var out bytes.Buffer
	a, err := New(strings.NewReader(""), &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Welcome(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "qual init") || !strings.Contains(out.String(), "reviewer") {
		t.Fatalf("welcome: %s", out.String())
	}
}

func TestBudgetLabelDistinguishesUnknownFromZero(t *testing.T) {
	if got := terminalBudget(&budgetView{CostKnown: false}); !strings.Contains(got, "unknown") {
		t.Fatal(got)
	}
	if got := terminalBudget(&budgetView{CostKnown: true}); !strings.Contains(got, "$0 spent") {
		t.Fatal(got)
	}
}

func TestInitMergePreservesExistingAccountsAndProjectSettings(t *testing.T) {
	existing := config.Settings{SchemaVersion: 1, Trust: "trusted", Connections: map[string]config.Connection{"old": {Kind: "anthropic"}}, Reviewers: []config.Agent{{ID: "old-reviewer", Provider: "old", Model: "old-model"}}, Leader: config.Agent{ID: "old-leader", Provider: "old", Model: "old-model"}, MCP: []string{"tools"}}
	added := initialSettings(InitOptions{Connection: "new", Kind: "openai", Model: "new-model"})
	result := mergeInitSettings(existing, added, true)
	if len(result.Connections) != 2 || result.Reviewers[0].Model != "old-model" || len(result.MCP) != 1 || result.Trust != "trusted" {
		t.Fatalf("init discarded existing choices: %+v", result)
	}
}

func TestMissingProjectProviderNamesRequiredModelAndOrigin(t *testing.T) {
	effective := config.Effective{Settings: config.Settings{Reviewers: []config.Agent{{ID: "security", Provider: "team-account", Model: "requested-model"}}}, Provenance: map[string]config.Origin{"reviewers": {Source: "repository", Path: ".qual/settings.yaml", Revision: "abc123"}}}
	err := configurationError(effective, invalid("agent connection is undefined"))
	for _, want := range []string{"team-account", "requested-model", ".qual/settings.yaml", "qual init --connection team-account --model requested-model --add-account"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("diagnostic missing %q: %v", want, err)
		}
	}
}

func TestUnknownPricingDoesNotDefaultToEnforceableUSDLimit(t *testing.T) {
	if modelPricingKnown(InitOptions{Kind: "chatgpt", Model: "gpt-6-luna"}) {
		t.Fatal("subscription pricing treated as API pricing")
	}
	if modelPricingKnown(InitOptions{Kind: "openai-compatible", Model: "custom"}) {
		t.Fatal("custom pricing invented")
	}
	if !modelPricingKnown(InitOptions{Kind: "openai", Model: "gpt-6-luna"}) {
		t.Fatal("catalog pricing lost")
	}
}

func TestMissingLeaderAndSetupProvidersKeepLeafOrigins(t *testing.T) {
	for _, field := range []string{"leader", "setup"} {
		e := config.Effective{Provenance: map[string]config.Origin{field + ".model": {Source: "repository", Path: ".qual/settings.yaml"}}}
		agent := config.Agent{ID: field, Provider: "required", Model: "needed"}
		if field == "leader" {
			e.Settings.Leader = agent
		} else {
			e.Settings.Setup = agent
		}
		if got := configurationError(e, invalid("undefined provider")).Error(); !strings.Contains(got, ".qual/settings.yaml") {
			t.Fatal(got)
		}
	}
}

func TestInitRetainsExplicitCustomModelCapabilities(t *testing.T) {
	settings := initialSettings(InitOptions{Connection: "custom", Kind: "openai-compatible", Model: "private-model", Capabilities: []string{"tools", "streaming"}})
	if len(settings.Connections["custom"].Capabilities) != 2 {
		t.Fatal("explicit capabilities lost")
	}
}

func TestSharedWizardBudgetRequiresVerifiedPricingForEveryRole(t *testing.T) {
	for _, options := range []InitOptions{
		{Kind: "openai", Model: "gpt-6-luna", LeaderModel: "private-leader"},
		{Kind: "openai", Model: "gpt-6-luna", SetupModel: "private-setup"},
		{Kind: "openai", Model: "gpt-6-luna", Capabilities: []string{"tools", "streaming"}},
	} {
		if modelPricingKnown(options) {
			t.Fatalf("unverified role pricing advertised: %+v", options)
		}
	}
}

func TestInitNewAccountReservesConfiguredMissingCredentialIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUAL_HOME", home)
	var output bytes.Buffer
	a, err := New(strings.NewReader(""), &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("schema_version: 1\nconnections:\n  existing:\n    kind: openai\n    credential: profile:openai\n")
	if err = os.WriteFile(filepath.Join(home, "settings.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := a.reservedAccountIDs(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if id := newAccountOptions("openai", profiles, false).Profile; id == "openai" {
		t.Fatal("new account rebound an existing configured reference")
	}
}
