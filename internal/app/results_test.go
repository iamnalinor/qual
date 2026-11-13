package app

import (
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestReportExitDisposition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state domain.RunState
		fail  bool
		code  domain.ErrorCode
	}{
		{name: "ordinary findings succeed", state: domain.RunState{Status: domain.StatusCompleted, Results: []domain.AgentResult{{Role: domain.RoleLeader, Status: domain.StatusCompleted, Findings: []domain.Finding{{Severity: domain.SeverityCritical}}}}}},
		{name: "critical gate uses leader", state: domain.RunState{Status: domain.StatusCompleted, Results: []domain.AgentResult{{Role: domain.RoleLeader, Status: domain.StatusCompleted, Findings: []domain.Finding{{Severity: domain.SeverityCritical}}}}}, fail: true, code: "critical_findings"},
		{name: "rejected primary critical does not fail", state: domain.RunState{Status: domain.StatusCompleted, Results: []domain.AgentResult{{Role: domain.RoleReviewer, Status: domain.StatusCompleted, Findings: []domain.Finding{{Severity: domain.SeverityCritical}}}, {Role: domain.RoleLeader, Status: domain.StatusCompleted}}}, fail: true},
		{name: "partial primary cannot look complete", state: domain.RunState{Status: domain.StatusCompleted, Results: []domain.AgentResult{{Role: domain.RoleReviewer, Status: domain.StatusPartial}, {Role: domain.RoleLeader, Status: domain.StatusCompleted}}}, code: domain.ErrIncomplete},
		{name: "stale fails", state: domain.RunState{Status: domain.StatusStaleSnapshot}, code: domain.ErrIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.CodeOf(resultError(tc.state, tc.fail)); got != tc.code {
				t.Fatalf("exit code %q want %q", got, tc.code)
			}
		})
	}
}
