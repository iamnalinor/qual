package cli

import (
	"github.com/iamnalinor/qual/internal/app"
	"github.com/spf13/cobra"
)

func (s *shell) authentication() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Manage explicit API-key and OAuth credential profiles"}
	o := app.LoginOptions{}
	login := &cobra.Command{Use: "login", Short: "Save a protected API key or authorize the selected OAuth route", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return s.run(cmd, func(a *app.App) error { return a.Login(cmd.Context(), o) })
	}}
	f := login.Flags()
	f.StringVar(&o.Kind, "kind", "", "Provider kind (chatgpt/codex select OAuth)")
	f.StringVar(&o.Profile, "profile", "", "Profile name")
	f.StringVar(&o.Env, "key-env", "", "Read the API key from this environment variable")
	f.BoolVar(&o.Stdin, "stdin", false, "Read the API key from standard input")
	f.BoolVar(&o.Device, "device", false, "Use explicit legacy Codex device authorization")
	login.MarkFlagsMutuallyExclusive("key-env", "stdin")
	list := &cobra.Command{Use: "list", Short: "List safe profile metadata", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return s.run(cmd, func(a *app.App) error { return a.AuthList(cmd.Context()) })
	}}
	logout := &cobra.Command{Use: "logout <profile>", Short: "Remove selected credentials", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return s.run(cmd, func(a *app.App) error { return a.Logout(cmd.Context(), args[0]) })
	}}
	cmd.AddCommand(login, list, logout)
	return cmd
}
