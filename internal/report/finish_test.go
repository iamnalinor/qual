package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestFinishRejectsSnapshotAndMissingDecisions(t *testing.T) {
	snap := domain.Snapshot{RunID: "run", DiffHash: "diff"}
	spec := domain.AgentSpec{ID: "leader", Role: domain.RoleLeader}
	files := map[string][]byte{"report": []byte("# Report"), "findings": []byte(`[]`), "manifest": []byte(`{"run_id":"run","diff_hash":"wrong"}`), "decisions": []byte(`[]`)}
	args := Finish{Kind: "leader", ReportPath: "report", FindingsPath: "findings", ManifestPath: "manifest", DecisionsPath: "decisions"}
	if _, err := Validate(args, files, snap, spec, []domain.Finding{{ID: "first"}}); err == nil {
		t.Fatal("accepted different snapshot")
	}
	files["manifest"] = []byte(`{"run_id":"run","diff_hash":"diff"}`)
	if _, err := Validate(args, files, snap, spec, []domain.Finding{{ID: "first"}}); err == nil {
		t.Fatal("accepted uncovered finding")
	}
	files["decisions"] = []byte(`[{"finding_id":"first","decision":"rejected","reason":"no reproduction"}]`)
	got, err := Validate(args, files, snap, spec, []domain.Finding{{ID: "first"}})
	if err != nil || got.Status != domain.StatusCompleted {
		t.Fatalf("finish %+v %v", got, err)
	}
	files["findings"], err = json.Marshal([]domain.Finding{{ID: "f", Severity: domain.SeverityCritical}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Validate(args, files, snap, spec, nil); err == nil {
		t.Fatal("accepted unsupported finding without evidence")
	}
}

func TestLeaderCanPreservePrimaryFindingAuthorship(t *testing.T) {
	finding := domain.Finding{ID: "primary-f", Title: "Failure", Description: "description", Scenario: "trigger", Consequence: "data loss", Severity: domain.SeverityImportant, ReviewerID: "primary", Confidence: 0.9, Anchor: domain.Anchor{Path: "code.go", Side: "right", Commit: "right", StartLine: 1, EndLine: 1}, Evidence: []domain.EvidenceRef{{Kind: "file", Path: "code.go"}}}
	findings, err := json.Marshal([]domain.Finding{finding})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"report": []byte("# Reviewed"), "findings": findings, "manifest": []byte(`{"run_id":"run","diff_hash":"diff"}`), "decisions": []byte(`[{"finding_id":"primary-f","decision":"accepted","reason":"reproduced"}]`)}
	args := Finish{Kind: "leader", ReportPath: "report", FindingsPath: "findings", ManifestPath: "manifest", DecisionsPath: "decisions"}
	if _, err = Validate(args, files, domain.Snapshot{RunID: "run", DiffHash: "diff", HeadCommit: "right"}, domain.AgentSpec{ID: "leader", Role: domain.RoleLeader}, []domain.Finding{finding}); err != nil {
		t.Fatal(err)
	}
}

func TestFindingValidationExplainsIdentityAndFields(t *testing.T) {
	f := domain.Finding{ID: "reviewer-1", ReviewerID: "guessed"}
	err := findingDetails(f, "reviewer")
	if err == nil || !strings.Contains(err.Error(), `reviewer_id`) || !strings.Contains(err.Error(), `"reviewer"`) || !strings.Contains(err.Error(), f.ID) {
		t.Fatalf("identity repair instructions missing: %v", err)
	}
	f.ReviewerID = "reviewer"
	err = findingDetails(f, "reviewer")
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("missing field not named: %v", err)
	}
	var finding domain.Finding
	err = Decode([]byte(`{"left_anchor":{}}`), &finding)
	if err == nil || !strings.Contains(err.Error(), "left_anchor") {
		t.Fatalf("unknown field not named: %v", err)
	}
}

func TestFindingFieldRepairErrors(t *testing.T) {
	base := domain.Finding{ID: "reviewer-1", ReviewerID: "reviewer", Title: "Failure", Description: "Bad division", Scenario: "Zero denominator", Consequence: "Panic", Severity: domain.SeverityImportant, Confidence: 0.9, Anchor: domain.Anchor{Path: "code.go", Side: "right", Commit: "head", StartLine: 1, EndLine: 1}, Evidence: []domain.EvidenceRef{{Kind: "file", Path: "code.go"}}}
	cases := []struct {
		name   string
		change func(*domain.Finding)
		field  string
	}{
		{"identity", func(f *domain.Finding) { f.ReviewerID = "wrong" }, "reviewer_id"},
		{"description", func(f *domain.Finding) { f.Description = "" }, "description"},
		{"scenario", func(f *domain.Finding) { f.Scenario = "" }, "scenario"},
		{"consequence", func(f *domain.Finding) { f.Consequence = "" }, "consequence"},
		{"severity", func(f *domain.Finding) { f.Severity = "bad" }, "severity"},
		{"confidence", func(f *domain.Finding) { f.Confidence = 2 }, "confidence"},
		{"evidence", func(f *domain.Finding) { f.Evidence = nil }, "evidence"},
		{"evidence reference", func(f *domain.Finding) { f.Evidence = []domain.EvidenceRef{{Kind: "file"}} }, "evidence"},
		{"anchor", func(f *domain.Finding) { f.Anchor.Commit = "wrong" }, "anchor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			tc.change(&f)
			err := validateFinding(f, "reviewer", domain.Snapshot{HeadCommit: "head"})
			if err == nil || !strings.Contains(err.Error(), f.ID) || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("repair needs finding ID and field %q: %v", tc.field, err)
			}
		})
	}
}
