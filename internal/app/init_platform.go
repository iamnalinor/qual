package app

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/iamnalinor/qual/internal/auth"
	"github.com/iamnalinor/qual/internal/tui"
)

func (a *App) configureGitLabHost(ctx context.Context, o InitOptions) (string, bool, error) {
	host, err := a.chooseGitLabHost(ctx, o.GitLabHost)
	if err != nil {
		return "", false, err
	}
	registry, err := a.platformHosts()
	if err != nil {
		return host, false, err
	}
	reference, token := "", ""
	if !o.GitLabPublic {
		reference, token, err = a.gitLabCredentialChoice(ctx, host, o.GitLabTokenEnv)
	}
	if err != nil {
		return host, false, err
	}
	registry.GitLab[host] = gitLabHost{Credential: reference}
	accepted, err := a.activateGitLabHost(ctx, host, registry, token, o.Accept)
	return host, accepted, err
}

func (a *App) chooseGitLabHost(ctx context.Context, host string) (string, error) {
	if host == "" {
		if a.terminal == nil {
			return "", invalid("Provide the trusted hostname with qual init --gitlab-host HOST, or run qual init in a terminal.")
		}
		var err error
		host, err = a.terminal.Ask(ctx, tui.Prompt{Title: a.text("GitLab hostname"), Description: a.text("Enter your trusted self-managed GitLab host, optionally with its HTTPS port.")})
		if err != nil {
			return "", err
		}
	}
	return gitLabAuthority(host)
}

func (a *App) activateGitLabHost(ctx context.Context, host string, registry platformHosts, token string, explicit bool) (bool, error) {
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return false, err
	}
	path := filepath.Join(a.Home, "platform-hosts.json")
	if err = a.proposal(ctx, "Trusted GitLab repository access", path, data); err != nil {
		return false, err
	}
	accepted, err := a.accept(ctx, explicit, "Trust this GitLab host and save its authentication?")
	if err != nil || !accepted {
		return accepted, err
	}
	if token != "" {
		if err = a.saveGitLabToken(ctx, host, registry.GitLab[host].Credential, token); err != nil {
			return false, err
		}
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = writePrivate(path, data); err != nil {
		return false, err
	}
	return true, nil
}

func validateInitPlatformOptions(o InitOptions) error {
	if (o.GitLabTokenEnv != "" || o.GitLabPublic) && o.GitLabHost == "" && o.Kind != "gitlab-host" {
		return invalid("GitLab authentication options require qual init --gitlab-host HOST.")
	}
	if o.GitLabHost != "" && o.Kind != "" && o.Kind != "gitlab-host" {
		return invalid("Configure repository access with qual init --gitlab-host HOST separately from model-provider setup (--kind).")
	}
	if o.GitLabPublic && o.GitLabTokenEnv != "" {
		return invalid("Choose either --gitlab-public or --gitlab-token-env NAME.")
	}
	return nil
}

func (a *App) gitLabCredentialChoice(ctx context.Context, host, environment string) (string, string, error) {
	if environment != "" {
		if !platformEnvName.MatchString(environment) {
			return "", "", invalid("Use an environment variable name with --gitlab-token-env NAME; do not put token values in command arguments.")
		}
		return "env:" + environment, "", nil
	}
	if a.terminal == nil {
		return "", "", invalid("Select a host-specific token with --gitlab-token-env NAME, or run qual init --gitlab-host " + commandArgument(host) + " in a terminal. For a public host, use --gitlab-public.")
	}
	token, err := a.terminal.Ask(ctx, tui.Prompt{Title: a.text("GitLab access token"), Description: a.text("Paste a token issued by this host. Leave empty for public repositories. The token stays hidden."), Secret: true})
	if err != nil || token == "" {
		return "", "", err
	}
	id, err := runID()
	if err != nil {
		return "", "", err
	}
	return "profile:gitlab_" + string(id), token, nil
}

func (a *App) saveGitLabToken(ctx context.Context, host, reference, token string) error {
	service, err := a.authService()
	if err != nil {
		return err
	}
	return service.Save(ctx, auth.Credentials{ID: reference[len("profile:"):], Kind: "gitlab", Subject: host, Issuer: "https://" + host, AccessToken: token})
}

func (a *App) renderGitLabHost(host string, accepted bool) error {
	if a.JSON {
		return a.Output(map[string]any{"accepted": accepted, "gitlab_host": host})
	}
	if !accepted {
		return a.line("%s\n", a.text("GitLab host preview only. Repository access was not changed."))
	}
	return a.line("%s: %s\n\n%s\n  qual review https://%s/group/repo/-/merge_requests/NUMBER\n", a.text("Trusted GitLab host saved"), host, a.text("Review a merge request from this host:"), host)
}
