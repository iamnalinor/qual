package cli

import (
	"time"

	"github.com/iamnalinor/qual/internal/app"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/spf13/cobra"
)

func (s *shell) sessions() *cobra.Command {
	cmd := &cobra.Command{Use: "sessions", Short: "Inspect and manage durable sessions"}
	cmd.AddCommand(&cobra.Command{Use: "list", Short: "List sessions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return s.run(cmd, func(a *app.App) error { return a.Sessions(cmd.Context()) })
	}})
	for _, name := range []string{"show", "cancel", "resume", "export"} {
		var output string
		child := &cobra.Command{Use: name + " <run-id>", Short: name + " a persisted session", Args: cobra.ExactArgs(1)}
		child.RunE = func(cmd *cobra.Command, args []string) error {
			return s.run(cmd, func(a *app.App) error {
				id := domain.RunID(args[0])
				switch name {
				case "show":
					return a.Show(cmd.Context(), id)
				case "cancel":
					return a.Cancel(cmd.Context(), id)
				case "resume":
					return a.Resume(cmd.Context(), id, output)
				default:
					return a.Export(cmd.Context(), id, output)
				}
			})
		}
		if name == "resume" || name == "export" {
			child.Flags().StringVar(&output, "output", "", "Report directory or export archive file")
		}
		cmd.AddCommand(child)
	}
	return cmd
}

func (s *shell) configuration() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect strict effective configuration"}
	for _, name := range []string{"show", "validate"} {
		var path string
		child := &cobra.Command{Use: name, Short: name + " effective settings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return s.run(cmd, func(a *app.App) error {
				e, err := a.Config(cmd.Context(), path)
				if err != nil {
					return err
				}
				return a.DisplayConfig(e, name == "validate")
			})
		}}
		child.Flags().StringVar(&path, "path", "", "Resolve project configuration at this target")
		cmd.AddCommand(child)
	}
	return cmd
}

func (s *shell) models() *cobra.Command {
	var connection string
	cmd := &cobra.Command{Use: "models", Short: "Inspect provider model capabilities and known prices"}
	list := &cobra.Command{Use: "list", Short: "List selected connection models", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return s.run(cmd, func(a *app.App) error { return a.Models(cmd.Context(), connection) })
	}}
	list.Flags().StringVar(&connection, "connection", "", "Configured connection name")
	cmd.AddCommand(list)
	return cmd
}

func (s *shell) mcp() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Probe authorized MCP connections without invoking tools"}
	for _, name := range []string{"list", "test"} {
		var path string
		child := &cobra.Command{Use: name + " [server]", Short: name + " server schemas and authorized tools", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			server := ""
			if len(args) > 0 {
				server = args[0]
			}
			return s.run(cmd, func(a *app.App) error { return a.MCP(cmd.Context(), path, server) })
		}}
		child.Flags().StringVar(&path, "path", "", "Resolve project server selections")
		cmd.AddCommand(child)
	}
	return cmd
}

func (s *shell) cleanup() *cobra.Command {
	var ttl time.Duration
	cmd := &cobra.Command{Use: "cleanup", Short: "Remove all terminal, unpinned disposable worktrees", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return s.run(cmd, func(a *app.App) error { return a.Cleanup(cmd.Context(), ttl) })
	}}
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Optional minimum age of terminal worktrees (default: all ages)")
	return cmd
}

func (s *shell) doctor() *cobra.Command {
	var connections bool
	cmd := &cobra.Command{Use: "doctor", Short: "Check dependencies and optional unpaid connection probes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return s.run(cmd, func(a *app.App) error { return a.Doctor(cmd.Context(), connections) })
	}}
	cmd.Flags().BoolVar(&connections, "connections", false, "Verify selected provider authentication without inference")
	return cmd
}
