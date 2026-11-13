package cli

import (
	"github.com/iamnalinor/qual/internal/app"
	"github.com/spf13/cobra"
)

func (s *shell) review() *cobra.Command {
	o := app.ReviewOptions{}
	cmd := &cobra.Command{Use: "review [target]", Short: "Review local changes or a GitHub/GitLab review URL", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		o.Source.Target = target(args)
		return s.run(cmd, func(a *app.App) error { return a.Review(cmd.Context(), o) })
	}}
	f := cmd.Flags()
	f.BoolVar(&o.Source.Staged, "staged", false, "Review the index")
	f.BoolVar(&o.Source.Worktree, "worktree", false, "Review tracked working files")
	f.BoolVar(&o.Source.IncludeUntracked, "include-untracked", false, "Include untracked working files")
	f.StringVar(&o.Source.Base, "base", "", "Left commit reference")
	f.StringVar(&o.Source.Head, "head", "", "Right commit reference (requires base)")
	f.StringVar(&o.ExpectedHead, "expected-head", "", "Require the authorized original source SHA")
	f.StringVar(&o.ExpectedTarget, "expected-target", "", "Require the authorized target tip SHA")
	f.StringVar(&o.Output, "output", "", "Write report.json and report.md in this directory")
	f.BoolVar(&o.FailOnCritical, "fail-on-critical", false, "Exit nonzero when leader confirms critical findings")
	f.StringVar(&o.Focus, "focus", "", "Reviewer focus preset")
	f.StringVar(&o.Instructions, "instructions", "", "Additional reviewer instructions")
	f.StringVar(&o.Reasoning, "reasoning-effort", "", "Reviewer reasoning effort")
	f.StringVar(&o.Investigation, "investigation-effort", "", "Reviewer investigation effort")
	f.StringVar(&o.UILanguage, "ui-language", "", "CLI language preference")
	f.StringVar(&o.ReviewLanguage, "review-language", "", "Review report language")
	cmd.MarkFlagsMutuallyExclusive("staged", "worktree", "head")
	return cmd
}

func (s *shell) setup() *cobra.Command {
	o := app.SetupOptions{}
	cmd := &cobra.Command{Use: "setup [path]", Short: "Investigate project and preview personal settings and rules", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		o.Target = target(args)
		return s.run(cmd, func(a *app.App) error { return a.Setup(cmd.Context(), o) })
	}}
	cmd.Flags().StringVar(&o.Trust, "trust", "", "Choose trusted or untrusted before model exploration")
	cmd.Flags().BoolVar(&o.Accept, "accept", false, "Accept validated proposal into personal project settings")
	cmd.Flags().StringVar(&o.Output, "output", "", "Write setup reports in this directory")
	return cmd
}

func (s *shell) init() *cobra.Command {
	o := app.InitOptions{}
	cmd := &cobra.Command{Use: "init", Short: "Verify a provider connection and preview user defaults", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return s.run(cmd, func(a *app.App) error { return a.Init(cmd.Context(), o) })
	}}
	f := cmd.Flags()
	f.StringVar(&o.GitLabHost, "gitlab-host", "", "Configure trusted self-managed GitLab repository access for HOST[:PORT]")
	f.StringVar(&o.GitLabTokenEnv, "gitlab-token-env", "", "Host-specific GitLab token environment variable name; no plaintext token argument")
	f.BoolVar(&o.GitLabPublic, "gitlab-public", false, "Trust this GitLab host for public repositories without a token")
	cmd.MarkFlagsMutuallyExclusive("gitlab-token-env", "gitlab-public")

	f.StringVar(&o.Connection, "connection", "", "Connection name")
	f.StringVar(&o.Kind, "kind", "", "Explicit provider and billing route")
	f.StringVar(&o.Endpoint, "endpoint", "", "Explicit trusted endpoint")
	f.StringVar(&o.Credential, "credential", "", "env:NAME or profile:NAME reference")
	f.BoolVar(&o.Device, "device", false, "Use device authorization for the explicit legacy codex route")
	f.StringSliceVar(&o.Capabilities, "capability", nil, "Explicit verified custom model capability (tools, streaming, reasoning); repeatable")
	f.StringVar(&o.Model, "model", "", "Reviewer model")
	f.StringVar(&o.LeaderModel, "leader-model", "", "Independent leader model")
	f.StringVar(&o.SetupModel, "setup-model", "", "Setup model")
	f.StringVar(&o.Reasoning, "reasoning-effort", "", "Default reasoning effort")
	f.StringVar(&o.Soft, "soft", "", "Per-role soft budget in USD")
	f.StringVar(&o.Hard, "hard", "", "Per-role hard budget in USD")
	f.BoolVar(&o.Accept, "accept", false, "Activate verified settings")
	f.BoolVar(&o.AddOnly, "add-account", false, "Keep current review defaults while adding this account")
	return cmd
}
