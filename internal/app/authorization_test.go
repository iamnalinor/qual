package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthorizationJSONAndHumanModes(t *testing.T) {
	for _, code := range []string{"", "FIXTURE-CODE"} {
		var out bytes.Buffer
		a := &App{Err: &out, JSON: true}
		if err := a.authorization("https://auth.example/fixture", code); err != nil {
			t.Fatal(err)
		}
		var event struct {
			SchemaVersion   int `json:"schema_version"`
			Type, URL, Code string
		}
		if err := json.Unmarshal(out.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event.SchemaVersion != 1 || event.Type != "authorization" || event.URL != "https://auth.example/fixture" || event.Code != code {
			t.Fatalf("event: %+v", event)
		}
		out.Reset()
		a.JSON = false
		if err := a.authorization("https://auth.example/fixture", code); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "https://auth.example/fixture") || !strings.Contains(out.String(), code) {
			t.Fatal(out.String())
		}
	}
}
