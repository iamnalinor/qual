package app

import (
	"fmt"
	"strings"

	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
)

var focusPresets = map[string]string{
	"general":         "Prioritize correctness, regressions, contracts and code quality.",
	"correctness":     "Investigate logic, edge cases, state transitions, concurrency and error handling.",
	"architecture":    "Check module boundaries, dependencies, public contracts and existing architecture.",
	"maintainability": "Check project conventions, duplication, excessive complexity and unnecessary abstractions.",
	"security":        "Investigate trust boundaries, inputs, secrets, authorization, injection and unsafe defaults.",
	"tests":           "Check tests of changed behavior, meaningful assertions and missing regression coverage.",
}

func guidance(agents []domain.AgentSpec, e config.Effective) []domain.AgentSpec {
	rules := scopedRules(e)
	result := append([]domain.AgentSpec(nil), agents...)
	for i := range result {
		agent := &result[i]
		if agent.InvestigationEffort == "" {
			agent.InvestigationEffort = "light"
		}
		agent.Instructions += "\n" + e.Guidance + "\nScoped instructions (apply only within the recorded scopes and globs):\n" + rules
		if preset, ok := focusPresets[agent.Focus]; ok {
			agent.Instructions += "\n" + preset + " Report obvious serious defects outside this focus as well."
		}
		agent.Instructions += investigationGuidance(agent.InvestigationEffort) + uiGuidance(e.Settings.UILanguage)
	}
	return result
}

func investigationGuidance(effort string) string {
	switch effort {
	case "light":
		return "\nPrefer read-only investigation. Run commands when they materially confirm a finding."
	case "full":
		return "\nPerform focused tests and reproductions. Explain expensive or state-changing commands through visible actions before executing them."
	default:
		return ""
	}
}

func scopedRules(e config.Effective) string {
	var out strings.Builder
	for _, rule := range e.Rules {
		fmt.Fprintf(&out, "%+v\n", rule)
	}
	return out.String()
}
