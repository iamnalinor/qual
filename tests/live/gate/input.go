package main

import (
	"encoding/json"
	"errors"
)

// Only inline text, function traffic and opaque reasoning continuation are allowed.
// References to stored responses, files, images, audio or hosted tool calls could
// add unbounded input or separately priced effects outside the serialized bound.
func validateInput(raw json.RawMessage) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return errors.New("inline input required")
	}
	for _, item := range items {
		if err := validateItem(item); err != nil {
			return err
		}
	}
	return nil
}

func validateItem(item map[string]json.RawMessage) error {
	var kind string
	if data, ok := item["type"]; ok {
		if json.Unmarshal(data, &kind) != nil {
			return errors.New("invalid input type")
		}
	}
	switch kind {
	case "", "message":
		return validateContent(item["content"])
	case "function_call_output":
		var output *string
		if err := json.Unmarshal(item["output"], &output); err != nil || output == nil {
			return errors.New("inline string function output required")
		}
		return nil
	case "function_call", "reasoning":
		return nil
	default:
		return errors.New("external or hosted input forbidden")
	}
}

func validateContent(raw json.RawMessage) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return errors.New("inline text content required")
	}
	for _, part := range parts {
		if part.Type != "input_text" && part.Type != "output_text" {
			return errors.New("nontext content forbidden")
		}
	}
	return nil
}
