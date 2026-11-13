package report

import (
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestVisibleTranscriptRedactsOpaqueData(t *testing.T) {
	messages := []domain.Message{{Role: "assistant", Content: "Visible evidence", ProviderData: []byte(`{"encrypted":"private-fixture"}`)}}
	data, err := VisibleTranscript(messages)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-fixture") || !strings.Contains(string(data), "Visible evidence") {
		t.Fatalf("bad transcript redaction %s", data)
	}
	if !strings.Contains(string(messages[0].ProviderData), "private-fixture") {
		t.Fatal("redaction mutated private messages")
	}
}
