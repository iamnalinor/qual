package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestJournalCheckpointArtifactsAndLock(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	s := New(home)
	state := domain.RunState{SchemaVersion: 1, RunID: "run"}
	if err := s.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	verifyOwnerLock(ctx, t, home, s)
	var err error
	if err = s.Append(ctx, domain.Event{RunID: "run", Type: "intent"}); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, "run")
	if err != nil || len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("events %v: %v", events, err)
	}
	verifyEvidenceArtifact(ctx, t, s)
	state.JournalSequence = 1
	if err = s.Save(ctx, state); err != nil {
		t.Fatal(err)
	}
	got, err := New(home).Load(ctx, "run")
	if err != nil || got.JournalSequence != 1 {
		t.Fatalf("load %v: %v", got, err)
	}
	if err = s.Create(ctx, state); err == nil {
		t.Fatal("overwrote existing run")
	}
	if _, err = s.Load(ctx, "../escape"); err == nil {
		t.Fatal("accepted path traversal")
	}
	verifyTornJournal(ctx, t, home, s)
}

func TestCancelExportAndRemoveAreDurable(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	s := New(home)
	state := domain.RunState{SchemaVersion: 1, RunID: "management", Status: domain.StatusCompleted}
	if err := s.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestCancel(ctx, state.RunID); err != nil {
		t.Fatal(err)
	}
	cancelled, err := New(home).Cancelled(ctx, state.RunID)
	if err != nil || !cancelled {
		t.Fatalf("cancel marker: %v %v", cancelled, err)
	}
	var output strings.Builder
	if err = s.Export(ctx, state.RunID, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"schema_version":1`) {
		t.Fatalf("missing export schema %s", output.String())
	}
	if err = s.ClearCancel(ctx, state.RunID); err != nil {
		t.Fatal(err)
	}
	if err = s.Remove(ctx, state.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(ctx, state.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session remains: %v", err)
	}
}

func verifyEvidenceArtifact(ctx context.Context, t *testing.T, s *Store) {
	t.Helper()
	artifact, err := s.PutArtifact(ctx, "run", "output", strings.NewReader("evidence"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(artifact.Path)
	if err != nil || string(data) != "evidence" {
		t.Fatal(err)
	}
}

func verifyTornJournal(ctx context.Context, t *testing.T, home string, s *Store) {
	t.Helper()
	// A torn final append is recoverable; complete preceding records remain authoritative.
	f, err := os.OpenFile(filepath.Join(home, "sessions", "run", "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // Test-owned fixed journal fixture path.
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"sequence":2`); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, "run")
	if err != nil || len(events) != 1 {
		t.Fatalf("torn journal %v: %v", events, err)
	}
}

func verifyOwnerLock(ctx context.Context, t *testing.T, home string, s *Store) {
	t.Helper()
	unlock, err := s.Lock(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New(home).Lock(ctx, "run"); err == nil {
		t.Fatal("second owner acquired lock")
	}
	if err = unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestExportRedactsPrivateContinuationsWithoutChangingResumeJournal(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	state := domain.RunState{RunID: "private-export"}
	if err := s.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	private := `{"provider_state":{"opaque":"private-fixture"},"messages":[{"role":"assistant","content":"visible","provider_data":{"opaque":"private-fixture"}}]}`
	if err := s.Append(ctx, domain.Event{RunID: state.RunID, Type: "agent_checkpoint", Data: []byte(private)}); err != nil {
		t.Fatal(err)
	}
	artifact, err := s.PutArtifact(ctx, state.RunID, "legacy-transcript", strings.NewReader(private))
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err = s.Export(ctx, state.RunID, &output); err != nil {
		t.Fatal(err)
	}
	var archive struct {
		Events         []domain.Event    `json:"events"`
		Artifacts      map[string][]byte `json:"artifacts"`
		ArtifactSHA256 map[string]string `json:"artifact_sha256"`
	}
	if err = json.Unmarshal([]byte(output.String()), &archive); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(archive.Events[0].Data), "private-fixture") || strings.Contains(string(archive.Artifacts[artifact.ID]), "private-fixture") {
		t.Fatal("export exposed private continuation")
	}
	if invalidExportDigest(archive.ArtifactSHA256[artifact.ID], artifact.SHA256) {
		t.Fatal("redacted export did not identify its transformed digest")
	}
	events, err := s.Events(ctx, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events[0].Data), "private-fixture") {
		t.Fatal("export destroyed private resume journal")
	}
}

func invalidExportDigest(exported, original string) bool {
	return exported == "" || exported == original
}
