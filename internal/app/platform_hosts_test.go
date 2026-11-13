package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/auth"
)

func TestGitLabRegistryNullMapIsSafeToConfigure(t *testing.T) {
	registry, err := decodePlatformHosts(strings.NewReader(`{"schema_version":1,"gitlab_hosts":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if registry.GitLab == nil {
		t.Fatal("decoded host registry cannot accept initialization")
	}
}

func TestGitLabCancelledAcceptedInitDoesNotPersist(t *testing.T) {
	home := t.TempDir()
	var output bytes.Buffer
	a := &App{Home: home, JSON: true, Out: &output, Err: &output}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := a.activateGitLabHost(ctx, "gitlab.example.com", platformHosts{SchemaVersion: 1, GitLab: map[string]gitLabHost{"gitlab.example.com": {}}}, "", true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled init: %v", err)
	}
	if _, err = os.Stat(filepath.Join(home, "platform-hosts.json")); !os.IsNotExist(err) {
		t.Fatalf("cancelled host configuration persisted: %v", err)
	}
}

func TestGitLabSavedCredentialCannotCrossHostBoundary(t *testing.T) {
	t.Setenv("QUAL_AUTH_BACKEND", "file")
	a := &App{Home: t.TempDir()}
	service, err := a.authService()
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Save(t.Context(), auth.Credentials{ID: "host_a", Kind: "gitlab", Subject: "gitlab.a.example", AccessToken: "fixture-host-token"}); err != nil {
		t.Fatal(err)
	}
	token, err := a.gitLabToken(t.Context(), "gitlab.b.example", "profile:host_a")
	if err == nil || token != "" || strings.Contains(err.Error(), "fixture-host-token") {
		t.Fatalf("host token boundary: %v", err)
	}
}
