package report

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/iamnalinor/qual/internal/domain"
)

// VisibleTranscript preserves visible messages while removing opaque provider continuity.
// Private checkpoints retain those fields for exact provider replay.
func VisibleTranscript(messages []domain.Message) ([]byte, error) {
	data, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("encode transcript: %w", err)
	}
	visible, _, err := RedactProviderData(data)
	return visible, err
}

// RedactProviderData removes reserved private-continuity fields from JSON views.
// Non-JSON artifact content is returned unchanged, as are already public JSON bytes.
func RedactProviderData(data []byte) ([]byte, bool, error) {
	if !json.Valid(data) {
		return data, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false, fmt.Errorf("decode transcript view: %w", err)
	}
	if !redactOpaque(value) {
		return data, false, nil
	}
	visible, err := json.Marshal(value)
	if err != nil {
		return nil, false, fmt.Errorf("encode transcript view: %w", err)
	}
	return visible, true, nil
}

func redactOpaque(value any) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "provider_state" || key == "provider_data" {
				delete(typed, key)
				changed = true
				continue
			}
			if redactOpaque(item) {
				changed = true
			}
		}
	case []any:
		for _, item := range typed {
			if redactOpaque(item) {
				changed = true
			}
		}
	default:
	}
	return changed
}
