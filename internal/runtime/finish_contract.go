package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/report"
)

// completionContract accepts no primary results: model-produced data must stay
// in user messages, including identities used in concrete leader examples.
func completionContract(snapshot domain.Snapshot, spec domain.AgentSpec) (string, error) {
	example, err := encodeCompletionExample(&domain.RunState{Snapshot: snapshot}, spec)
	if err != nil {
		return "", err
	}
	return "Your exact agent ID is " + string(spec.ID) + ". New findings must use reviewer_id equal to this ID and globally unique IDs prefixed with it. " +
		"Use only documented JSON fields; optional fields may be omitted. Evidence also permits tool_call_id/artifact_id; decisions permit merged_into for merges; findings.json is an array ([] for no supported findings). severity must be critical/important/minor; confidence must be 0..1. title, description, scenario and consequence must be nonempty. Write each files entry as its own file, write a nonempty report.md, then call finish with the finish object. This is a shape example, not evidence: replace illustrative findings with verified findings and populate manifest evidence/checked_files. Evidence needs kind and at least one of path/tool_call_id/artifact_id. Each finding has exactly one anchor (never left_anchor/right_anchor); side right uses snapshot head_commit, left uses base_commit, with positive inclusive line numbers. " +
		roleCompletionGuidance(spec.Role) + "\nCompletion JSON example:\n" + example + "\nEnd completion example. ", nil
}

// encodeCompletionExample uses the same JSON types as finish validation.
func encodeCompletionExample(state *domain.RunState, spec domain.AgentSpec) (string, error) {
	finding := completionFinding(state.Snapshot, spec.ID)
	decisions := []domain.FindingDecision{}
	if spec.Role == domain.RoleLeader {
		for _, result := range state.Results {
			for _, primary := range result.Findings {
				decision := domain.FindingDecision{FindingID: primary.ID, Decision: "rejected", Reason: "Example only: replace with your evidence-based disposition"}
				if len(decisions) == 0 {
					finding.ID = primary.ID
					finding.ReviewerID = primary.ReviewerID
					decision.Decision = "accepted"
					decision.Reason = "Example only: independently verify before accepting"
				}
				decisions = append(decisions, decision)
			}
		}
	}
	files := map[string]any{
		"findings.json": []domain.Finding{finding},
		"manifest.json": report.Manifest{RunID: state.Snapshot.RunID, DiffHash: state.Snapshot.DiffHash, CheckedFiles: []string{}, Evidence: []domain.EvidenceRef{}, Questions: []string{}, Assumptions: []string{}},
	}
	finish := report.Finish{Kind: string(spec.Role), ReportPath: "report.md", FindingsPath: "findings.json", ManifestPath: "manifest.json"}
	switch spec.Role {
	case domain.RoleLeader:
		files["decisions.json"] = decisions
		finish.DecisionsPath = "decisions.json"
	case domain.RoleSetup:
		files["proposals.json"] = map[string]any{"settings": map[string]any{}, "rules": []string{}, "commands": []string{}, "reasons": []string{}, "questions": []string{}}
		finish.ProposalsPath = "proposals.json"
	case domain.RoleReviewer:
	}
	example, err := json.Marshal(struct {
		AgentID domain.AgentID `json:"agent_id"`
		Files   map[string]any `json:"files"`
		Finish  report.Finish  `json:"finish"`
	}{spec.ID, files, finish})
	if err != nil {
		return "", fmt.Errorf("encode completion example: %w", err)
	}
	return string(example), nil
}

func completionFinding(snapshot domain.Snapshot, agent domain.AgentID) domain.Finding {
	path := "code.go"
	if len(snapshot.Files) > 0 {
		path = snapshot.Files[0].Path
	}
	return domain.Finding{ID: string(agent) + "-1", ReviewerID: agent, Title: "Example finding title", Description: "Example causal explanation", Scenario: "Example triggering input or action", Consequence: "Example observable consequence", Severity: domain.SeverityImportant, Confidence: 0.9, Anchor: domain.Anchor{Path: path, Side: "right", Commit: snapshot.HeadCommit, StartLine: 1, EndLine: 1}, Evidence: []domain.EvidenceRef{{Kind: "file", Path: path}}, Assumptions: []string{}}
}

func roleCompletionGuidance(role domain.Role) string {
	switch role {
	case domain.RoleLeader:
		return "Read every saved primary result. Retained primary findings keep their original id AND reviewer_id. New leader findings use your own ID. decisions.json must cover each primary ID exactly once: finding_id is the primary ID; accepted requires that same ID in final findings; merged requires merged_into naming a final finding ID; rejected/unresolved need no final finding. Every decision requires reason."
	case domain.RoleSetup:
		return "proposals.json requires a settings object and rules/commands/reasons/questions string arrays. These are proposals only; runtime applies user-accepted changes outside this loop."
	case domain.RoleReviewer:
		return ""
	}
	return ""
}
