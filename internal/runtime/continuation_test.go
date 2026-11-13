package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

const continuationFixture = `[{"type":"opaque","encrypted_content":"continuation-private-fixture"}]`

type continuationProvider struct {
	t     *testing.T
	calls int
}

func (*continuationProvider) Models(context.Context) ([]domain.ModelInfo, error) { return nil, nil }
func (p *continuationProvider) Run(_ context.Context, request domain.ModelRequest, emit func(domain.ModelEvent) error) error {
	p.calls++
	if len(request.ProviderState) > 0 {
		p.t.Fatal("opaque continuation duplicated in request-global state")
	}
	found := 0
	for _, message := range request.Messages {
		if len(message.ProviderData) > 0 {
			found++
			if message.Role != "assistant" || string(message.ProviderData) != continuationFixture {
				p.t.Fatalf("continuation attached to wrong message: %+v", message)
			}
		}
	}
	if p.calls == 2 && found != 1 {
		p.t.Fatalf("assistant continuation replayed %d times", found)
	}
	if err := emit(domain.ModelEvent{Type: "text", Text: "Visible review progress."}); err != nil {
		return err
	}
	if p.calls == 1 {
		return emit(domain.ModelEvent{Type: "completed", ProviderState: json.RawMessage(continuationFixture)})
	}
	return nil
}

func TestContinuationRoundtripIsPrivateAndExactlyOnce(t *testing.T) {
	provider := &continuationProvider{t: t}
	rt, state, p := requestFixture(t, provider)
	ctx := context.Background()
	if err := rt.turn(ctx, &state, p); err != nil {
		t.Fatal(err)
	}
	assistant := p.Messages[len(p.Messages)-1]
	if string(assistant.ProviderData) != continuationFixture || len(p.ProviderState) != 0 {
		t.Fatalf("continuation was not attached privately: %+v", assistant)
	}
	restored, err := rt.recover(ctx, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	p = restored[p.Spec.ID]
	if err = rt.turn(ctx, &state, p); err != nil {
		t.Fatal(err)
	}
	partialResult, stopErr := rt.stopAgent(ctx, &state, p, coded(domain.ErrIncomplete, "fixture pause", nil))
	if domain.CodeOf(stopErr) != domain.ErrIncomplete {
		t.Fatalf("fixture stop: %v", stopErr)
	}
	verifyVisibleArtifacts(ctx, t, rt, state.RunID, partialResult.Artifacts)
	restored, err = rt.recover(ctx, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored[p.Spec.ID].Messages[2].ProviderData) != continuationFixture {
		t.Fatal("redaction destroyed private resume continuation")
	}
}

func verifyVisibleArtifacts(ctx context.Context, t *testing.T, rt *Runtime, id domain.RunID, artifacts []domain.Artifact) {
	t.Helper()
	for _, artifact := range artifacts {
		data, e := rt.readArtifact(ctx, id, []byte(`{"id":"`+artifact.ID+`"}`))
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(data, "continuation-private-fixture") {
			t.Fatal("human-visible transcript exposed opaque continuation")
		}
	}
}
