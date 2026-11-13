package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamnalinor/qual/internal/diagnostic"
)

func runGit(ctx context.Context, root string, extra []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// #nosec G204 -- fixed executable, no shell, validated immutable IDs and trusted HTTPS URLs.
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "http.followRedirects=false"}, args...)...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = time.Second
	command.Dir = root
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0")
	command.Env = append(command.Env, extra...)

	var output, stderr bytes.Buffer
	command.Stdout = &output
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", failure("source_unavailable", "git "+args[0]+" failed", diagnostic.GitCause(err, stderr.String(), command.Env))
	}
	return strings.TrimSpace(output.String()), nil
}

func exactObjects(ctx context.Context, root string, metadata ReviewContext) bool {
	for _, id := range []string{metadata.Head, metadata.TargetSHA} {
		if !completeRevision(ctx, root, id) {
			return false
		}
	}
	shallow, err := runGit(ctx, root, nil, "rev-parse", "--is-shallow-repository")
	return err == nil && shallow == "false"
}

func (s *Source) repository(ctx context.Context, metadata ReviewContext) (string, func() error, error) {
	noop := func() error { return nil }
	if s.config.Checkout != "" && s.completeCheckout(ctx, metadata) {
		return s.config.Checkout, noop, nil
	}
	if s.config.Home == "" {
		return "", noop, failure("invalid_config", "hosted review requires a storage home", nil)
	}
	parent := filepath.Join(s.config.Home, "repos")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", noop, err
	}
	root, err := os.MkdirTemp(parent, "fetch-")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() error { return os.RemoveAll(root) }
	if _, err = runGit(ctx, root, nil, "init", "--bare", "."); err != nil {
		return "", noop, errors.Join(err, cleanup())
	}
	if err = s.fetch(ctx, root, metadata, 32); err != nil {
		return "", noop, errors.Join(err, cleanup())
	}
	return root, cleanup, nil
}

func (s *Source) fetch(ctx context.Context, root string, metadata ReviewContext, depth int) error {
	environment := []string{}
	if s.config.GitCAFile != "" {
		environment = append(environment, "GIT_SSL_CAINFO="+s.config.GitCAFile)
	}
	if token := s.config.Tokens[metadata.Target.Host]; token != "" {
		password := "x-access-token:" + token
		if metadata.Target.Kind == "gitlab" {
			password = "oauth2:" + token
		}
		environment = append(environment, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(password)))
	}
	for _, side := range []struct{ url, id string }{{metadata.TargetURL, metadata.TargetSHA}, {metadata.HeadURL, metadata.Head}} {
		if _, err := runGit(ctx, root, environment, "fetch", "--no-auto-maintenance", "--no-tags", "--no-write-fetch-head", "--depth="+strconv.Itoa(depth), "--", side.url, side.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) mergeBase(ctx context.Context, root string, metadata ReviewContext) (string, error) {
	for depth := 32; depth <= 32768; depth *= 2 {
		bases, err := runGit(ctx, root, nil, "merge-base", "--all", metadata.TargetSHA, metadata.Head)
		ids := strings.Fields(bases)
		if err == nil && len(ids) == 1 && sha(ids[0]) && certifiedBase(ctx, root, ids[0]) {
			return ids[0], nil
		}
		if len(ids) > 1 {
			return "", failure("source_unavailable", "multiple merge bases require explicit resolution", nil)
		}
		shallow, e := runGit(ctx, root, nil, "rev-parse", "--is-shallow-repository")
		if e != nil {
			return "", e
		}
		if shallow != "true" {
			break
		}
		if err = s.fetch(ctx, root, metadata, depth*2); err != nil {
			return "", err
		}
	}
	return "", failure("source_unavailable", "unique merge base unavailable within bounded history", nil)
}

// A visible common ancestor is conclusive only when every shallow frontier is
// already its ancestor. Other frontiers could conceal another best base.
func certifiedBase(ctx context.Context, root, base string) bool {
	// #nosec G304 -- root is an explicit trusted checkout or Qual-owned bare repository.
	boundaries, err := os.ReadFile(filepath.Join(root, "shallow"))
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	for _, boundary := range strings.Fields(string(boundaries)) {
		if _, err = runGit(ctx, root, nil, "merge-base", "--is-ancestor", boundary, base); err != nil {
			return false
		}
	}
	return true
}

func completeRevision(ctx context.Context, root, id string) bool {
	out, err := runGit(ctx, root, nil, "rev-parse", "--verify", id+"^{commit}")
	if err != nil || out != id {
		return false
	}
	_, err = runGit(ctx, root, nil, "rev-list", "--objects", "--missing=error", "--no-walk", id, "--")
	return err == nil
}

func (s *Source) completeCheckout(ctx context.Context, metadata ReviewContext) bool {
	root := s.config.Checkout
	if !exactObjects(ctx, root, metadata) {
		return false
	}
	base, err := s.mergeBase(ctx, root, metadata)
	return err == nil && completeRevision(ctx, root, base)
}
