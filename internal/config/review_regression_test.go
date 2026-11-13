package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

const (
	cappedUser           = "connections:\n  main:\n    kind: openai\nreviewers:\n  - id: first\n    provider: main\n    model: small\n    hard: '2'\n  - id: removed\n    provider: main\n    model: small\n    hard: '3'\nleader:\n  id: lead\n  provider: main\n  model: small\n"
	replacementReviewers = "reviewers:\n  - id: first\n    provider: main\n    model: changed\n"
)

func TestRestrictedListReplacementPreservesExternalHardCaps(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "settings.yaml"), cappedUser)
	repo, rev := repository(t, map[string]string{".qual/settings.yaml": replacementReviewers})
	req := Request{Home: home, Snapshot: domain.Snapshot{StorePath: repo, RulesRevision: rev, ProjectID: "p"}}
	e, err := Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if e.Provenance["reviewers.first.hard"].Source != "user" {
		t.Fatal("lost cap authorization provenance")
	}
	if len(e.Agents) != 2 || e.Agents[0].ID != "first" || e.Agents[0].Budget.Hard != 2000000000 {
		t.Fatalf("cap lost or list accumulated: %+v", e.Agents)
	}
	repo, rev = repository(t, map[string]string{".qual/settings.yaml": strings.ReplaceAll(replacementReviewers, "first", "renamed")})
	req.Snapshot.StorePath = repo
	req.Snapshot.RulesRevision = rev
	if _, err = Resolve(context.Background(), req); err == nil {
		t.Fatal("renaming bypassed external cap")
	}
}

func TestResolveAndDiscoverReadRecoverableGenerationUnderWriterLock(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "projects", "p")
	write(t, filepath.Join(root, "settings.yaml"), "trust: trusted\nreview_language: fr\n")
	write(t, filepath.Join(root, "rules", "a.md"), "personal")
	unlock, err := lockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(root, root+".config.backup"); err != nil {
		t.Fatal(err)
	}
	req := Request{Home: home, Snapshot: domain.Snapshot{ProjectID: "p"}}
	if _, err = Resolve(context.Background(), req); err == nil {
		t.Fatal("Resolve silently read rename gap")
	}
	if _, err = Discover(context.Background(), home, req.Snapshot, nil); err == nil {
		t.Fatal("Discover silently read rename gap")
	}
	if err = unlock(); err != nil {
		t.Fatal(err)
	}
	assertRecoveredGeneration(t, req)
}

func assertRecoveredGeneration(t *testing.T, req Request) {
	t.Helper()
	home := req.Home
	e, err := Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if e.Settings.Trust != "trusted" || e.Settings.ReviewLanguage != "fr" || len(e.Rules) != 1 {
		t.Fatalf("lost recovered settings/rules: %+v", e)
	}
	root := filepath.Join(home, "projects", "p")
	if err = os.Rename(root, root+".config.backup"); err != nil {
		t.Fatal(err)
	}
	d, err := Discover(context.Background(), home, req.Snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Rules) != 1 {
		t.Fatalf("lost recovered discovery: %+v", d)
	}
}

func TestDepthPrunedPersonalRulesHaveBoundedWarning(t *testing.T) {
	home := t.TempDir()
	deep := strings.Repeat("nested/", maxRulesDepth+2)
	write(t, filepath.Join(home, "rules", deep, "a.md"), "deep")
	d, err := Discover(context.Background(), home, domain.Snapshot{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Rules) != 0 || len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "depth") {
		t.Fatalf("missing bounded depth warning: %+v", d)
	}
}

func TestApplyValidatesPreparedRepositoryAndPersistentContext(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "settings.yaml"), "connections:\n  main:\n    kind: openai\n")
	repo, rev := repository(t, map[string]string{".qual/settings.yaml": "reviewers:\n  - id: reviewer\n    provider: main\n    model: small\nleader:\n  id: lead\n  provider: main\n  model: small\n"})
	req := Request{Home: home, Snapshot: domain.Snapshot{StorePath: repo, RulesRevision: rev, ProjectID: "p"}, CLI: []byte("leader:\n  id: transient\n  provider: main\n  model: small\n")}
	proposal := ApplyRequest{Home: home, ProjectID: "p", Accepted: true, Context: req, Proposal: Proposal{Settings: []byte("leader: null")}}
	if err := Apply(context.Background(), proposal); err == nil {
		t.Fatal("saved invalid repository configuration masked by transient CLI")
	}
	if _, err := os.Stat(filepath.Join(home, "projects", "p", "settings.yaml")); !os.IsNotExist(err) {
		t.Fatal("invalid proposal activated")
	}
	proposal.Proposal.Settings = []byte("review_language: fr")
	if err := Apply(context.Background(), proposal); err != nil {
		t.Fatal(err)
	}
	req.CLI = nil
	e, err := Resolve(context.Background(), req)
	if err != nil || len(e.Agents) != 2 || e.Settings.ReviewLanguage != "fr" {
		t.Fatalf("persistent config invalid: %+v %v", e, err)
	}
}

func TestProposalReviewerReplacementCannotRemoveExternalCaps(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "settings.yaml"), cappedUser)
	req := Request{Home: home, Snapshot: domain.Snapshot{ProjectID: "p"}}
	apply := ApplyRequest{Home: home, ProjectID: "p", Accepted: true, Context: req, Proposal: Proposal{Settings: []byte(replacementReviewers)}}
	if err := Apply(context.Background(), apply); err != nil {
		t.Fatal(err)
	}
	e, err := Resolve(context.Background(), req)
	if err != nil || len(e.Agents) != 2 || e.Agents[0].Budget.Hard != 2000000000 {
		t.Fatalf("proposal dropped caps or accumulated list: %+v %v", e.Agents, err)
	}
	apply.Proposal.Settings = []byte(strings.ReplaceAll(replacementReviewers, "first", "renamed"))
	if err = Apply(context.Background(), apply); err == nil {
		t.Fatal("proposal renamed capped reviewer")
	}
}

func TestDiscoveryWarningsRemainBounded(t *testing.T) {
	home := t.TempDir()
	rules := filepath.Join(home, "rules")
	if err := os.MkdirAll(rules, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 140 {
		if err := os.Symlink("missing", filepath.Join(rules, fmt.Sprintf("%03d.md", i))); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Discover(context.Background(), home, domain.Snapshot{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Warnings) != 128 {
		t.Fatalf("unbounded or absent warnings: %d", len(d.Warnings))
	}
}

func TestApplyRequiresMatchingPreparedSnapshot(t *testing.T) {
	for _, project := range []domain.ProjectID{"", "other"} {
		home := t.TempDir()
		err := Apply(context.Background(), ApplyRequest{Home: home, ProjectID: "p", Accepted: true, Context: Request{Snapshot: domain.Snapshot{ProjectID: project}}, Proposal: Proposal{Rules: map[string]string{"a.md": "a"}}})
		if err == nil {
			t.Fatalf("accepted mismatched snapshot identity %q", project)
		}
	}
}

func TestSequentialProposalsPreserveCapsOutsideReviewerSelection(t *testing.T) {
	for _, origin := range []string{"user", "personal"} {
		t.Run(origin, func(t *testing.T) {
			home, req, _ := capOriginFixture(t, origin)
			apply := ApplyRequest{Home: home, ProjectID: "p", Context: req, Accepted: true, Proposal: Proposal{Settings: []byte("reviewers: []")}}
			if err := Apply(context.Background(), apply); err != nil {
				t.Fatal(err)
			}
			e, err := Resolve(context.Background(), req)
			if err != nil || len(e.Agents) != 1 {
				t.Fatalf("removed reviewers resurrected: %+v %v", e.Agents, err)
			}
			apply.Proposal.Settings = []byte(replacementReviewers)
			if err = Apply(context.Background(), apply); err != nil {
				t.Fatal(err)
			}
			e, err = Resolve(context.Background(), req)
			if err != nil || len(e.Agents) != 2 || e.Agents[0].Budget.Hard != 2000000000 {
				t.Fatalf("sequential proposal bypassed %s cap: %+v %v", origin, e.Agents, err)
			}
		})
	}
}

func TestTrustedLimitUpdatesOverrideHistoricalAuthorization(t *testing.T) {
	for _, origin := range []string{"user", "personal"} {
		t.Run(origin, func(t *testing.T) {
			home, req, authorityPath := capOriginFixture(t, origin)
			apply := ApplyRequest{Home: home, ProjectID: "p", Context: req, Accepted: true, Proposal: Proposal{Settings: []byte("reviewers: []")}}
			if err := Apply(context.Background(), apply); err != nil {
				t.Fatal(err)
			}
			updated := strings.ReplaceAll(cappedUser, "hard: '2'", "hard: '4'")
			write(t, authorityPath, updated)
			apply.Proposal.Settings = []byte(replacementReviewers)
			if err := Apply(context.Background(), apply); err != nil {
				t.Fatal(err)
			}
			e, err := Resolve(context.Background(), req)
			if err != nil || e.Agents[0].Budget.Hard != 4000000000 {
				t.Fatalf("trusted %s update frozen: %+v %v", origin, e.Agents, err)
			}
		})
	}
}

func TestInheritedUserCapsFollowLaterTrustedUpdates(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "settings.yaml"), cappedUser)
	req := Request{Home: home, Snapshot: domain.Snapshot{ProjectID: "p"}}
	apply := ApplyRequest{Home: home, ProjectID: "p", Context: req, Accepted: true}
	for _, settings := range []string{"reviewers: []", replacementReviewers, "reviewers: []", replacementReviewers} {
		apply.Proposal.Settings = []byte(settings)
		if err := Apply(context.Background(), apply); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(home, "settings.yaml"), strings.ReplaceAll(cappedUser, "hard: '2'", "hard: '4'"))
	e, err := Resolve(context.Background(), req)
	if err != nil || e.Agents[0].Budget.Hard != 4000000000 {
		t.Fatalf("copied user cap became permanent personal cap: %+v %v", e.Agents, err)
	}
	apply.Proposal.Settings = []byte("review_language: fr")
	if err = Apply(context.Background(), apply); err != nil {
		t.Fatal(err)
	}
	e, err = Resolve(context.Background(), req)
	if err != nil || e.Agents[0].Budget.Hard != 4000000000 {
		t.Fatalf("normalizing inherited cap failed: %+v %v", e.Agents, err)
	}
	personal := strings.ReplaceAll(replacementReviewers, "model: changed", "model: changed\n    hard: '1'")
	write(t, filepath.Join(home, "projects", "p", "settings.yaml"), personal)
	e, err = Resolve(context.Background(), req)
	if err != nil || e.Agents[0].Budget.Hard != 1000000000 {
		t.Fatalf("explicit personal cap lost precedence: %+v %v", e.Agents, err)
	}
}

func capOriginFixture(t *testing.T, origin string) (string, Request, string) {
	t.Helper()
	home := t.TempDir()
	userPath := filepath.Join(home, "settings.yaml")
	write(t, userPath, "connections:\n  main:\n    kind: openai\n")
	authorityPath := map[string]string{"user": userPath, "personal": filepath.Join(home, "projects", "p", "settings.yaml")}[origin]
	write(t, authorityPath, cappedUser)
	return home, Request{Home: home, Snapshot: domain.Snapshot{ProjectID: "p"}}, authorityPath
}

func TestTrustedCIOverridesInheritedCapWithoutChangingPersonalAuthority(t *testing.T) {
	home, req, _ := capOriginFixture(t, "user")
	apply := ApplyRequest{Home: home, ProjectID: "p", Context: req, Accepted: true, Proposal: Proposal{Settings: []byte(replacementReviewers)}}
	if err := Apply(context.Background(), apply); err != nil {
		t.Fatal(err)
	}
	req.TrustedCI = []byte(strings.ReplaceAll(replacementReviewers, "model: changed", "model: changed\n    hard: '7'"))
	e, err := Resolve(context.Background(), req)
	if err != nil || e.Agents[0].Budget.Hard != 7000000000 {
		t.Fatalf("CI cap override lost precedence: %+v %v", e.Agents, err)
	}
	apply.Context = req
	apply.Proposal.Settings = []byte("review_language: fr")
	if err = Apply(context.Background(), apply); err != nil {
		t.Fatal(err)
	}
	req.TrustedCI = nil
	e, err = Resolve(context.Background(), req)
	if err != nil || e.Agents[0].Budget.Hard != 2000000000 {
		t.Fatalf("transient CI authority persisted: %+v %v", e.Agents, err)
	}
	if e.Provenance["reviewers.first.hard"].Source != "user" {
		t.Fatal("inherited cap provenance lost original authority")
	}
}
