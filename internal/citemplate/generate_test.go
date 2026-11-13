package citemplate

import (
	"strings"
	"testing"
)

func TestGeneratedWorkflowsPreserveAuthorizationBoundary(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			files, err := Generate(provider)
			if err != nil {
				t.Fatal(err)
			}
			requireContains(t, files.Workflow, []string{"ci authorize --provider " + provider, "--expected-head", "--expected-target", "--network restricted", "--credential", "QUAL_SOURCE_SHA"})
			if strings.Index(files.Workflow, "ci authorize") > strings.Index(files.Workflow, "printf '%s' \"$OPENAI_API_KEY\"") {
				t.Error("model credential file populated before authorization")
			}
			rejectContains(t, files.Workflow, []string{"pull_request:", "--publish", "${{ github.event.comment.body }}", "${{ github.event.pull_request.body }}"})
			requireContains(t, files.Setup, []string{"is public", "iamnalinor/qual", "QUAL_SOURCE_READ_TOKEN", "40-character", "fixture"})
		})
	}
}

func requireContains(t *testing.T, value string, required []string) {
	t.Helper()
	for _, part := range required {
		if !strings.Contains(value, part) {
			t.Errorf("missing execution/setup contract %q", part)
		}
	}
}

func rejectContains(t *testing.T, value string, forbidden []string) {
	t.Helper()
	for _, part := range forbidden {
		if strings.Contains(value, part) {
			t.Errorf("unsafe generated command %q", part)
		}
	}
}
