package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/session"
)

func TestLargeCommandOutputPreservesFullArtifact(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	store := session.New(home)
	state := domain.RunState{RunID: "large"}
	if err := store.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	rt := New(Config{Home: home, Store: store, MaxOutputBytes: 1024})
	p := &agentProgress{Workspace: t.TempDir()}
	outcome := rt.executeTool(ctx, &state, p, domain.ToolCall{ID: "output", Name: "shell", Arguments: json.RawMessage(`{"command":"head -c 5000000 /dev/zero | tr '\\000' x"}`)})
	if outcome.Result.IsError || !outcome.Result.Truncated || outcome.Result.ArtifactID == "" {
		t.Fatalf("output was not preserved: %+v", outcome.Result)
	}
	artifact, err := store.Artifact(ctx, state.RunID, outcome.Result.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Size != 5000000 {
		t.Fatalf("lost full output: %d bytes", artifact.Size)
	}
	data, err := rt.readArtifact(ctx, state.RunID, []byte(`{"id":"`+artifact.ID+`","offset":4999990,"limit":10}`))
	if err != nil || data != strings.Repeat("x", 10) {
		t.Fatalf("range %q: %v", data, err)
	}
}
