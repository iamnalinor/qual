package citemplate

import (
	"embed"
	"fmt"
)

//go:embed templates/*
var templates embed.FS

// Files holds editable workflow YAML and its required installation instructions.
type Files struct {
	Workflow string
	Setup    string
}

// Generate returns a concrete provider workflow and setup instructions.
func Generate(provider string) (Files, error) {
	if provider != "github" && provider != "gitlab" {
		return Files{}, fmt.Errorf("ci_unsupported: unknown provider %q", provider)
	}
	workflow, err := templates.ReadFile("templates/" + provider + ".yml")
	if err != nil {
		return Files{}, fmt.Errorf("ci_template: load workflow: %w", err)
	}
	setup, err := templates.ReadFile("templates/" + provider + ".md")
	if err != nil {
		return Files{}, fmt.Errorf("ci_template: load setup: %w", err)
	}
	return Files{Workflow: string(workflow), Setup: string(setup)}, nil
}
