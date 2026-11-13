package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func git(t *testing.T, repo string, args ...string) string {
	t.Helper()
	// #nosec G204 -- Integration helper runs git with discrete fixture arguments, without a shell.
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", repo}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
	return string(b)
}

func repository(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "test@example.invalid")
	git(t, repo, "config", "user.name", "Test")
	for p, s := range files {
		write(t, filepath.Join(repo, p), s)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "fixture")
	return repo, git(t, repo, "rev-parse", "HEAD")[:40]
}

func TestRulesUseFixedRevisionAndScopes(t *testing.T) {
	repo, rev := repository(t, map[string]string{"AGENTS.md": "root", "pkg/AGENTS.md": "nested", ".qual/rules/style.md": "style", ".qual/settings.yaml": "review_language: en"})
	write(t, filepath.Join(repo, "AGENTS.md"), "hostile new head")
	write(t, filepath.Join(repo, ".qual/settings.yaml"), "trust: trusted")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "hostile")
	home := t.TempDir()
	write(t, filepath.Join(home, "rules", "personal.md"), "personal")
	e, err := Resolve(context.Background(), Request{Home: home, Snapshot: domain.Snapshot{StorePath: repo, RulesRevision: rev, ProjectID: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if e.Settings.Trust != "untrusted" || len(e.Rules) != 4 {
		t.Fatalf("wrong fixed config/rules: %+v", e)
	}
	for _, r := range e.Rules {
		if r.Content == "hostile new head" {
			t.Fatal("used HEAD")
		}
		if r.Path == "pkg/AGENTS.md" && (!r.Applies("pkg/x.go") || r.Applies("else/x.go")) {
			t.Fatal("lost nested scope")
		}
		if r.SHA256 == "" {
			t.Fatal("missing hash")
		}
	}
}

func TestAdditionalGlobAndSymlinkRules(t *testing.T) {
	repo, _ := repository(t, map[string]string{"docs/policy.md": "scoped custom rule", "AGENTS.md": "root"})
	if err := os.Symlink("../AGENTS.md", filepath.Join(repo, "docs", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "symlink")
	rev := git(t, repo, "rev-parse", "HEAD")[:40]
	d, err := Discover(context.Background(), t.TempDir(), domain.Snapshot{StorePath: repo, RulesRevision: rev}, []InstructionFile{{Path: "docs/policy.md", Glob: "*.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Rules) != 2 || len(d.Warnings) != 1 {
		t.Fatalf("wrong discovery: %+v", d)
	}
	for _, r := range d.Rules {
		if r.Path == "docs/policy.md" && (!r.Applies("docs/a.go") || r.Applies("docs/a.md") || r.Applies("src/a.go")) {
			t.Fatal("glob scope was lost")
		}
	}
}
