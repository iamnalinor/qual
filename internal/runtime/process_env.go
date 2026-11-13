package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultToolPath = "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"

func processEnvironment(home, toolPath string) ([]string, error) {
	if toolPath == "" {
		toolPath = defaultToolPath
	}
	for _, dir := range filepath.SplitList(toolPath) {
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("trusted tool path entries must be absolute")
		}
	}
	paths := map[string]string{"HOME": home, "XDG_CACHE_HOME": filepath.Join(home, "cache"), "GOCACHE": filepath.Join(home, "cache", "go-build"), "GOMODCACHE": filepath.Join(home, "cache", "go-mod"), "GOPATH": filepath.Join(home, "go"), "TMPDIR": filepath.Join(home, "tmp")}
	env := []string{"PATH=" + toolPath, "LANG=C.UTF-8", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GOENV=off", "GOTOOLCHAIN=local"}
	for name, path := range paths {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("prepare controlled process directory: %w", err)
		}
		env = append(env, name+"="+path)
	}
	return env, nil
}

func (r *Runtime) roleProcessEnvironment(workspace string) ([]string, error) {
	if !filepath.IsAbs(r.config.Home) {
		return nil, fmt.Errorf("absolute runtime home required for process tools")
	}
	sum := sha256.Sum256([]byte(workspace))
	home := filepath.Join(r.config.Home, "process", hex.EncodeToString(sum[:]), "home")
	return processEnvironment(home, r.config.ToolPath)
}

func processExecutable(name string, env []string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	var search string
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			search = strings.TrimPrefix(entry, "PATH=")
			break
		}
	}
	for _, dir := range filepath.SplitList(search) {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("find command in trusted tool path: %w", exec.ErrNotFound)
}
