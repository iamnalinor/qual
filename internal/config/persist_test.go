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

func TestAcceptedPersistencePreservesMetadataAndRejectsSymlinks(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "projects", "p")
	write(t, filepath.Join(root, "metadata.json"), "preserved")
	write(t, filepath.Join(root, "rules", "old.md"), "old")
	request := ApplyRequest{Home: home, ProjectID: "p", Context: Request{Snapshot: domain.Snapshot{ProjectID: "p"}}, Accepted: true, Trust: "untrusted", Proposal: Proposal{Settings: []byte("review_language: fr"), Rules: map[string]string{"new.md": "new"}}}
	if err := Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"metadata.json": "preserved", "rules/old.md": "old", "rules/new.md": "new"} {
		// #nosec G304 -- Test fixture paths are confined to t.TempDir.
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil || string(b) != want {
			t.Fatalf("lost %s: %s %v", p, b, err)
		}
	}
	// #nosec G304 -- Test fixture path is confined to t.TempDir.
	before, err := os.ReadFile(filepath.Join(root, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	request.Proposal.Settings = []byte("connections: {}")
	if err = Apply(context.Background(), request); err == nil {
		t.Fatal("accepted privilege proposal")
	}
	// #nosec G304 -- Test fixture path is confined to t.TempDir.
	after, err := os.ReadFile(filepath.Join(root, "settings.yaml"))
	if err != nil || string(before) != string(after) {
		t.Fatal("invalid proposal changed settings")
	}
	request.Proposal.Settings = nil
	request.Proposal.Rules = map[string]string{"bad/link.md": "bad"}
	if err = os.Symlink(t.TempDir(), filepath.Join(root, "rules", "bad")); err != nil {
		t.Fatal(err)
	}
	if err = Apply(context.Background(), request); err == nil {
		t.Fatal("accepted symlink destination")
	}
}

func TestDeclinedAndCancelledProposalsDoNotPersist(t *testing.T) {
	home := t.TempDir()
	request := ApplyRequest{Home: home, ProjectID: "p", Context: Request{Snapshot: domain.Snapshot{ProjectID: "p"}}, Proposal: Proposal{Rules: map[string]string{"a.md": "a"}}}
	if err := Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "projects", "p")); !os.IsNotExist(err) {
		t.Fatal("declined proposal persisted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request.Accepted = true
	if err := Apply(ctx, request); err == nil {
		t.Fatal("cancelled proposal persisted")
	}
}

func TestFailedSecondOutputLeavesSettingsUnchanged(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "projects", "p")
	write(t, filepath.Join(root, "settings.yaml"), "review_language: en\n")
	write(t, filepath.Join(root, "rules"), "blocks directory creation")
	err := Apply(context.Background(), ApplyRequest{Home: home, ProjectID: "p", Context: Request{Snapshot: domain.Snapshot{ProjectID: "p"}}, Accepted: true, Proposal: Proposal{Settings: []byte("review_language: fr"), Rules: map[string]string{"new.md": "rule"}}})
	if err == nil {
		t.Fatal("expected second output failure")
	}
	// #nosec G304 -- Fixture path is confined to t.TempDir.
	b, readErr := os.ReadFile(filepath.Join(root, "settings.yaml"))
	if readErr != nil || string(b) != "review_language: en\n" {
		t.Fatalf("changed settings despite failure: %s %v", b, readErr)
	}
}

func TestInvalidEffectiveProposalRejectedBeforeSave(t *testing.T) {
	cases := []struct{ settings, message string }{
		{"schema_version: 99", "unsupported settings schema version"},
		{"leader:\n  id: lead\n  provider: missing\n  model: small", "agent connection is undefined"},
		{"instruction_files:\n  - path: ../escape", "unsafe instruction path"},
	}
	for _, test := range cases {
		err := Apply(context.Background(), ApplyRequest{Home: t.TempDir(), ProjectID: "p", Context: Request{Snapshot: domain.Snapshot{ProjectID: "p"}}, Accepted: true, Proposal: Proposal{Settings: []byte(test.settings)}})
		if err == nil || !strings.Contains(err.Error(), test.message) {
			t.Fatalf("expected %q for %q, got %v", test.message, test.settings, err)
		}
	}
}

func TestRecoverInterruptedDirectoryInstall(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "projects", "p")
	write(t, filepath.Join(root+".config.backup", "settings.yaml"), "review_language: en\n")
	request := ApplyRequest{Home: home, ProjectID: "p", Context: Request{Snapshot: domain.Snapshot{ProjectID: "p"}}, Accepted: true, Proposal: Proposal{Rules: map[string]string{"new.md": "new"}}}
	if err := Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- Fixture path is confined to t.TempDir.
	b, err := os.ReadFile(filepath.Join(root, "settings.yaml"))
	if err != nil || !strings.Contains(string(b), "en") {
		t.Fatalf("did not recover previous settings: %s %v", b, err)
	}
	if _, err = os.Stat(root + ".config.backup"); !os.IsNotExist(err) {
		t.Fatal("backup retained after recovery")
	}
}

func TestPreviewShowsReadableSettingsAndEnforcesTotalBounds(t *testing.T) {
	b, err := Preview(Proposal{Settings: []byte("review_language: fr"), Rules: map[string]string{"a.md": "rule"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "review_language: fr") {
		t.Fatalf("unreadable preview: %s", b)
	}
	rules := map[string]string{}
	for i := range 9 {
		rules[fmt.Sprintf("%d.md", i)] = strings.Repeat("a", maxFileSize)
	}
	if _, err = Preview(Proposal{Rules: rules}); err == nil {
		t.Fatal("accepted oversized combined proposal")
	}
}

func TestConcurrentApplyUsesProjectLock(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "projects", "p")
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	request := ApplyRequest{Home: home, ProjectID: "p", Context: Request{Snapshot: domain.Snapshot{ProjectID: "p"}}, Accepted: true, Proposal: Proposal{Rules: map[string]string{"a.md": "a"}}}
	if err = Apply(context.Background(), request); err == nil {
		t.Fatal("concurrent writer bypassed lock")
	}
	if err = unlock(); err != nil {
		t.Fatal(err)
	}
	if err = Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "settings.yaml"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("settings permissions not owner-only")
	}
}
