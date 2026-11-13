package provider

import (
	"encoding/json"
	"fmt"
)

// OpenRouter sends opaque reasoning details in indexed deltas. Preserve their
// original order and assemble only documented payload fragments, never emit them
// as visible text. Metadata such as IDs/type/format is assigned, not concatenated.
func (s *stream) reasoningDelta(parts []object) {
	for _, part := range parts {
		key := fmt.Sprintf("%d/%s", part.num("index"), part.str("type"))
		target := s.reasoning[key]
		if target == nil {
			target = make(object)
			s.reasoning[key] = target
			s.reasoningOrder = append(s.reasoningOrder, key)
		}
		for field, value := range part {
			switch field {
			case "data", "text", "summary", "signature":
				target[field] = raw(target.str(field) + part.str(field))
			default:
				target[field] = value
			}
		}
	}
}

func (s *stream) continuation() json.RawMessage {
	for _, key := range s.reasoningOrder {
		s.opaque = append(s.opaque, s.reasoning[key])
	}
	if len(s.opaque) == 0 {
		return nil
	}
	return raw(s.opaque)
}
