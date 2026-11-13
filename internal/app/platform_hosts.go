package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/platform"
)

type (
	gitLabHost struct {
		Credential string `json:"credential,omitempty"`
	}
	platformHosts struct {
		SchemaVersion int                   `json:"schema_version"`
		GitLab        map[string]gitLabHost `json:"gitlab_hosts"`
	}
)

var platformEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func gitLabAuthority(value string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(value))
	parsed, err := url.Parse("https://" + host)
	if err != nil || host == "" || parsed.Host != host || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", invalid("Use a GitLab hostname, optionally with a port, without a URL path: qual init --gitlab-host gitlab.example.com")
	}
	return host, nil
}

func (a *App) platformHosts() (platformHosts, error) {
	empty := platformHosts{SchemaVersion: 1, GitLab: map[string]gitLabHost{}}
	path := filepath.Join(a.Home, "platform-hosts.json")
	// #nosec G304 -- Fixed trusted local host registry; O_NOFOLLOW rejects symlinks atomically.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, fmt.Errorf("open trusted GitLab host configuration: %w", err)
	}
	defer file.Close() //nolint:errcheck // Read result determines the registry loading error.
	info, err := file.Stat()
	if err != nil {
		return empty, fmt.Errorf("inspect trusted GitLab host configuration: %w", err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || int64(owner.Uid) != int64(os.Getuid()) {
		return empty, invalid("Trusted GitLab host configuration must be an owner-only regular file.")
	}
	return decodePlatformHosts(file)
}

func decodePlatformHosts(input io.Reader) (platformHosts, error) {
	empty := platformHosts{SchemaVersion: 1, GitLab: map[string]gitLabHost{}}
	decoder := json.NewDecoder(io.LimitReader(input, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&empty); err != nil {
		return empty, fmt.Errorf("decode trusted GitLab host configuration: %w", err)
	}
	if err := validatePlatformHosts(empty); err != nil {
		return empty, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return empty, invalid("Trusted GitLab host configuration must contain one JSON object.")
	}
	if empty.GitLab == nil {
		empty.GitLab = map[string]gitLabHost{}
	}
	return empty, nil
}

func validatePlatformHosts(hosts platformHosts) error {
	if hosts.SchemaVersion != 1 {
		return invalid("Unsupported trusted platform host configuration version.")
	}
	for host, selected := range hosts.GitLab {
		normalized, err := gitLabAuthority(host)
		if err != nil || normalized != host {
			return invalid("Invalid hostname in trusted platform host configuration. Run qual init --help.")
		}
		if selected.Credential != "" && !validPlatformReference(selected.Credential) {
			return invalid("GitLab credentials must be environment or protected account references, never plaintext settings.")
		}
	}
	return nil
}

func validPlatformReference(ref string) bool {
	if name, ok := strings.CutPrefix(ref, "env:"); ok {
		return platformEnvName.MatchString(name)
	}
	if name, ok := strings.CutPrefix(ref, "profile:"); ok {
		return name != "" && strings.IndexFunc(name, func(r rune) bool {
			return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", r)
		}) < 0
	}
	return false
}

func (a *App) source(ctx context.Context, target string) (*platform.Source, error) {
	hosts, err := a.platformHosts()
	if err != nil {
		return nil, err
	}
	tokens := map[string]string{"github.com": os.Getenv("GITHUB_TOKEN"), "gitlab.com": os.Getenv("GITLAB_TOKEN")}
	names := make([]string, 0, len(hosts.GitLab))
	for host := range hosts.GitLab {
		names = append(names, host)
	}
	sort.Strings(names)
	if err = a.sourceHostToken(ctx, target, hosts, tokens); err != nil {
		return nil, err
	}
	return platform.New(platform.Config{Home: a.Home, GitLabHosts: names, Tokens: tokens, Checkout: os.Getenv("QUAL_CI_CHECKOUT")}), nil
}

func (a *App) sourceHostToken(ctx context.Context, target string, hosts platformHosts, tokens map[string]string) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return nil
	}
	selected, configured := hosts.GitLab[parsed.Host]
	if !configured {
		if parsed.Host != "gitlab.com" && parsed.Host != "github.com" {
			return invalid("GitLab host " + parsed.Host + " is not configured. Add trusted repository access with qual init --gitlab-host " + commandArgument(parsed.Host) + ", then rerun this command.")
		}
		return nil
	}
	token, err := a.gitLabToken(ctx, parsed.Host, selected.Credential)
	if err != nil {
		return &domain.Error{Code: domain.ErrAuth, Message: "GitLab credentials for " + parsed.Host + " are unavailable. Run qual init --gitlab-host " + commandArgument(parsed.Host) + " to reconnect this host.", Cause: err}
	}
	tokens[parsed.Host] = token
	return nil
}

func (a *App) gitLabToken(ctx context.Context, host, reference string) (string, error) {
	if reference == "" {
		return "", nil
	}
	if name, ok := strings.CutPrefix(reference, "env:"); ok {
		value := os.Getenv(name)
		if value == "" {
			return "", invalid("The host-specific token environment variable " + name + " is empty.")
		}
		return value, nil
	}
	service, err := a.authService()
	if err != nil {
		return "", err
	}
	record, err := service.Load(ctx, strings.TrimPrefix(reference, "profile:"))
	if err != nil {
		return "", err
	}
	if record.Kind != "gitlab" || record.Subject != host {
		return "", invalid("The saved token belongs to a different platform host.")
	}
	return record.AccessToken, nil
}
