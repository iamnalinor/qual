// Package report validates role-specific completion contracts without filesystem effects.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path"
	"strings"

	"github.com/iamnalinor/qual/internal/domain"
)

// Finish references agent-written outputs within its workspace.
type Finish struct {
	Kind          string `json:"kind"`
	ReportPath    string `json:"report_path"`
	FindingsPath  string `json:"findings_path"`
	ManifestPath  string `json:"manifest_path"`
	DecisionsPath string `json:"decisions_path,omitempty"`
	ProposalsPath string `json:"proposals_path,omitempty"`
}

// Manifest binds evidence to the immutable review snapshot.
type Manifest struct {
	RunID        domain.RunID         `json:"run_id"`
	DiffHash     string               `json:"diff_hash"`
	CheckedFiles []string             `json:"checked_files"`
	Evidence     []domain.EvidenceRef `json:"evidence"`
	Questions    []string             `json:"questions"`
	Assumptions  []string             `json:"assumptions"`
}

// Decode rejects unknown keys and trailing values.
func Decode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode contract: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("contract has trailing JSON")
	}
	return nil
}

// Validate checks current run identity, findings and full leader dispositions.
func Validate(f Finish, files map[string][]byte, snapshot domain.Snapshot, spec domain.AgentSpec, primary []domain.Finding) (domain.AgentResult, error) {
	result := domain.AgentResult{AgentID: spec.ID, Role: spec.Role, Status: domain.StatusCompleted}
	if f.Kind != string(spec.Role) {
		return result, invalid("finish kind does not match role")
	}
	if strings.TrimSpace(string(files[f.ReportPath])) == "" {
		return result, invalid("empty report")
	}
	var manifest Manifest
	if err := Decode(files[f.ManifestPath], &manifest); err != nil {
		return result, err
	}
	if manifest.RunID != snapshot.RunID || manifest.DiffHash != snapshot.DiffHash {
		return result, invalid("finish snapshot mismatch")
	}
	if err := Decode(files[f.FindingsPath], &result.Findings); err != nil {
		return result, err
	}
	if result.Findings == nil {
		return result, invalid("findings must be an array")
	}
	if err := validateFindings(result.Findings, spec, snapshot, primary); err != nil {
		return result, err
	}
	if err := validateRole(f, files, spec.Role, &result, primary); err != nil {
		return result, err
	}
	result.Report = string(files[f.ReportPath])
	result.CheckedFiles = manifest.CheckedFiles
	result.Evidence = manifest.Evidence
	result.Questions = manifest.Questions
	result.Assumptions = manifest.Assumptions
	return result, nil
}
func invalid(message string) error { return &domain.Error{Code: domain.ErrTool, Message: message} }
func validateFindings(findings []domain.Finding, spec domain.AgentSpec, snapshot domain.Snapshot, primary []domain.Finding) error {
	authors := map[string]domain.AgentID{}
	if spec.Role == domain.RoleLeader {
		for _, finding := range primary {
			authors[finding.ID] = finding.ReviewerID
		}
	}
	seen := map[string]bool{}
	for _, finding := range findings {
		if finding.ID == "" || seen[finding.ID] {
			return invalid(fmt.Sprintf("findings: id %q is empty or duplicated", finding.ID))
		}
		seen[finding.ID] = true
		agent := spec.ID
		if author, ok := authors[finding.ID]; ok {
			agent = author
		}
		if err := validateFinding(finding, agent, snapshot); err != nil {
			return err
		}
	}
	return nil
}

func validateFinding(f domain.Finding, agent domain.AgentID, snapshot domain.Snapshot) error {
	if err := findingDetails(f, agent); err != nil {
		return err
	}
	if err := validateAnchor(f.Anchor, snapshot); err != nil {
		return invalid(fmt.Sprintf("finding %q: anchor: %v", f.ID, err))
	}
	for _, e := range f.Evidence {
		if e.Kind == "" || (e.Path == "" && e.ToolCallID == "" && e.ArtifactID == "") {
			return invalid(fmt.Sprintf("finding %q: evidence requires kind and at least one of path, tool_call_id or artifact_id", f.ID))
		}
	}
	return nil
}

func validateAnchor(anchor domain.Anchor, snapshot domain.Snapshot) error {
	if invalidAnchorPath(anchor.Path) || anchor.StartLine < 1 || anchor.EndLine < anchor.StartLine {
		return invalid("path must be a relative repository path and start_line/end_line must form a positive inclusive range")
	}
	expected := snapshot.HeadCommit
	if anchor.Side == "left" {
		expected = snapshot.BaseCommit
	} else if anchor.Side != "right" {
		return invalid(fmt.Sprintf("side %q must be left or right", anchor.Side))
	}
	if anchor.Commit != expected {
		return invalid(fmt.Sprintf("commit %q must equal snapshot %s commit %q", anchor.Commit, anchor.Side, expected))
	}
	return nil
}

func validateRole(f Finish, files map[string][]byte, role domain.Role, result *domain.AgentResult, primary []domain.Finding) error {
	switch role {
	case domain.RoleLeader:
		if err := Decode(files[f.DecisionsPath], &result.Decisions); err != nil {
			return err
		}
		return validateDecisions(result.Decisions, primary, result.Findings)
	case domain.RoleSetup:
		return validateProposals(files[f.ProposalsPath])
	case domain.RoleReviewer:
		return nil
	default:
		return invalid("unsupported finish role")
	}
}

func validateDecisions(decisions []domain.FindingDecision, primary, final []domain.Finding) error {
	inputs := map[string]bool{}
	outputs := map[string]bool{}
	for _, f := range primary {
		if inputs[f.ID] {
			return invalid("duplicate primary finding ID")
		}
		inputs[f.ID] = true
	}
	for _, f := range final {
		outputs[f.ID] = true
	}
	seen := map[string]bool{}
	for _, d := range decisions {
		if !inputs[d.FindingID] || seen[d.FindingID] || d.Reason == "" {
			return invalid("unknown, repeated or unexplained finding decision")
		}
		seen[d.FindingID] = true
		if err := validateDecision(d, outputs); err != nil {
			return err
		}
	}
	if len(seen) != len(inputs) {
		return invalid("leader must decide every primary finding")
	}
	return nil
}

func validateProposals(data []byte) error {
	var proposals struct {
		Settings  map[string]any `json:"settings"`
		Rules     []string       `json:"rules"`
		Commands  []string       `json:"commands"`
		Reasons   []string       `json:"reasons"`
		Questions []string       `json:"questions"`
	}
	if err := Decode(data, &proposals); err != nil {
		return err
	}
	if proposals.Settings == nil {
		return invalid("setup proposals require settings object")
	}
	return nil
}

func validateDecision(d domain.FindingDecision, outputs map[string]bool) error {
	switch d.Decision {
	case "accepted":
		if !outputs[d.FindingID] {
			return invalid("accepted finding absent from final findings")
		}
	case "merged":
		if !outputs[d.MergedInto] {
			return invalid("merged target absent")
		}
	case "rejected", "unresolved":
	default:
		return invalid("invalid finding disposition")
	}
	return nil
}

func findingDetails(f domain.Finding, agent domain.AgentID) error {
	if f.ReviewerID != agent {
		return invalid(fmt.Sprintf("finding %q: reviewer_id %q must equal %q", f.ID, f.ReviewerID, agent))
	}
	for _, field := range []struct{ name, value string }{{"title", f.Title}, {"description", f.Description}, {"scenario", f.Scenario}, {"consequence", f.Consequence}} {
		if strings.TrimSpace(field.value) == "" {
			return invalid(fmt.Sprintf("finding %q: %s must be nonempty", f.ID, field.name))
		}
	}
	if f.Severity != domain.SeverityCritical && f.Severity != domain.SeverityImportant && f.Severity != domain.SeverityMinor {
		return invalid(fmt.Sprintf("finding %q: severity %q must be critical, important or minor", f.ID, f.Severity))
	}
	if math.IsNaN(f.Confidence) || f.Confidence < 0 || f.Confidence > 1 {
		return invalid(fmt.Sprintf("finding %q: confidence must be between 0 and 1", f.ID))
	}
	if len(f.Evidence) == 0 {
		return invalid(fmt.Sprintf("finding %q: evidence must be a nonempty array", f.ID))
	}
	return nil
}

func invalidAnchorPath(value string) bool {
	clean := path.Clean(value)
	return value == "" || path.IsAbs(value) || clean == ".." || strings.HasPrefix(clean, "../")
}
