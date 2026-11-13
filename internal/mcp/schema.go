package mcp

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/iamnalinor/qual/internal/domain"
)

// The SDK schema resolver checks references and constraints but currently accepts
// unknown JSON Schema type names. Reject those before exposing definitions.
func checkSchemaTypes(raw []byte) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return failure(domain.ErrTool, "invalid MCP schema JSON", err)
	}
	return checkSchema(value)
}

func checkSchema(value any) error {
	schema, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if value, ok := schema["type"]; ok {
		if err := checkType(value); err != nil {
			return err
		}
	}
	for _, key := range []string{"$defs", "definitions", "properties", "patternProperties", "dependentSchemas"} {
		children, ok := schema[key].(map[string]any)
		if !ok {
			continue
		}
		for _, child := range children {
			if err := checkSchema(child); err != nil {
				return err
			}
		}
	}
	return checkChildren(schema)
}

func checkChildren(schema map[string]any) error {
	for _, key := range []string{"items", "prefixItems", "allOf", "anyOf", "oneOf", "not", "if", "then", "else", "additionalProperties", "propertyNames", "unevaluatedProperties", "contains", "unevaluatedItems", "additionalItems", "contentSchema"} {
		value := schema[key]
		if children, ok := value.([]any); ok {
			for _, child := range children {
				if err := checkSchema(child); err != nil {
					return err
				}
			}
		} else if err := checkSchema(value); err != nil {
			return err
		}
	}
	return nil
}

func checkType(value any) error {
	if types, ok := value.([]any); ok {
		for _, kind := range types {
			if err := checkType(kind); err != nil {
				return err
			}
		}
		return nil
	}
	kind, ok := value.(string)
	if !ok || !slices.Contains([]string{"null", "boolean", "object", "array", "number", "integer", "string"}, kind) {
		return failure(domain.ErrTool, fmt.Sprintf("invalid MCP schema type %q", kind), nil)
	}
	return nil
}
