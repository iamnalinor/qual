// Package cli provides the Qual command shell.
package cli

import (
	"errors"

	"github.com/iamnalinor/qual/internal/app"
	"github.com/spf13/cobra"
)

type shell struct{ json, noninteractive bool }

func (s *shell) application(cmd *cobra.Command) (*app.App, error) {
	a, err := app.New(cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	if err == nil {
		a.JSON = s.json
		a.NonInteractive = s.noninteractive
	}
	return a, err
}

func (s *shell) run(cmd *cobra.Command, action func(*app.App) error) error {
	a, err := s.application(cmd)
	if err != nil {
		return err
	}
	var warningErr error
	if cmd.Name() != "cleanup" {
		warningErr = a.AutomaticCleanup(cmd.Context())
	}
	return a.InteractionError(errors.Join(action(a), warningErr))
}

// NewRootCommand creates a fresh command tree for each invocation.
func NewRootCommand() *cobra.Command {
	s := &shell{}
	root := &cobra.Command{Use: "qual", Short: "Qual: AI code review with mandatory leader verification", Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		return s.run(cmd, func(a *app.App) error { return a.Welcome(cmd.Context()) })
	}}
	root.PersistentFlags().BoolVar(&s.json, "json", false, "Print structured final output and JSONL progress")
	root.PersistentFlags().BoolVar(&s.noninteractive, "non-interactive", false, "Never prompt; use explicit choices or conservative defaults")
	root.AddCommand(s.review(), s.setup(), s.init(), s.sessions(), s.configuration(), s.authentication(), s.models(), s.mcp(), s.cleanup(), s.doctor(), s.ci())
	return root
}

func target(args []string) string {
	if len(args) == 0 {
		return "."
	}
	return args[0]
}
