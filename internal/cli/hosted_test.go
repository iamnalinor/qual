package cli_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/platform"
)

type hostedHistory struct{ repo, base, head, target, headTree string }

func TestExecutableHostedURLs(t *testing.T) {
	binary := buildCLI(t)
	history := hostedRepository(t)
	for _, kind := range []string{"github", "gitlab"} {
		t.Run(kind, func(t *testing.T) {
			hostedEnvironment(t, kind, history)
			provider, calls := fixture(t)
			defer provider.Close()
			home := t.TempDir()
			configure(t, home, provider.URL+"/v1")
			url := "https://github.com/owner/repo/pull/7"
			if kind == "gitlab" {
				url = "https://gitlab.com/owner/repo/-/merge_requests/7"
			}
			// A foreign working directory must not influence URL selection or repository rules.
			cwd := t.TempDir()
			writeHostedFile(t, cwd, "AGENTS.md", "FOREIGN_RULE_MUST_NOT_APPEAR")
			output := t.TempDir()
			out, diagnostic, err := invoke(t, binary, home, cwd, "", "review", url, "--json", "--non-interactive", "--output", output, "--expected-head", history.head, "--expected-target", history.target)
			if err != nil {
				t.Fatalf("hosted review: %v\n%s\n%s", err, out, diagnostic)
			}
			verifyHostedResult(t, out, diagnostic, output, kind, history)
			if calls.Load() < 11 {
				t.Fatalf("reviewer/leader runtime not exercised: %d calls", calls.Load())
			}
			verifyHostedMismatch(t, binary, home, cwd, url, history.base, calls)
		})
	}
}

func verifyHostedMismatch(t *testing.T, binary, home, cwd, url, base string, calls *atomic.Int64) {
	t.Helper()
	before := calls.Load()
	for _, flag := range []string{"--expected-head", "--expected-target"} {
		out, diagnostic, err := invoke(t, binary, home, cwd, "", "review", url, "--json", "--non-interactive", flag, base)
		if err == nil || !strings.Contains(out+diagnostic, `"code":"ci_snapshot"`) {
			t.Fatalf("%s accepted moved identity: %v %s %s", flag, err, out, diagnostic)
		}
		if calls.Load() != before {
			t.Fatalf("%s called model before identity rejection", flag)
		}
	}
}

func hostedRepository(t *testing.T) hostedHistory {
	t.Helper()
	repo := repository(t)
	base := hostedRevision(t, repo, "HEAD")
	git(t, repo, "checkout", "-qb", "target")
	writeHostedFile(t, repo, "AGENTS.md", "TARGET_RULE_FIXED_AT_T\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "target rules")
	target := hostedRevision(t, repo, "HEAD")
	git(t, repo, "checkout", "-qb", "feature", base)
	writeHostedFile(t, repo, "file.txt", "changed hosted head\n")
	writeHostedFile(t, repo, "AGENTS.md", "HEAD_RULE_MUST_NOT_APPEAR\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "reviewed head")
	return hostedHistory{repo: repo, base: base, target: target, head: hostedRevision(t, repo, "HEAD"), headTree: hostedRevision(t, repo, "HEAD^{tree}")}
}

func hostedRevision(t *testing.T, repo, ref string) string {
	t.Helper()
	// #nosec G204 -- Fixed Git executable and arguments in a test-owned repository.
	cmd := exec.CommandContext(t.Context(), "git", "rev-parse", ref)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func writeHostedFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func verifyHostedResult(t *testing.T, out, diagnostic, output, kind string, history hostedHistory) {
	t.Helper()
	var state domain.RunState
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		t.Fatal(err)
	}
	s := state.Snapshot
	if state.Status != domain.StatusCompleted || len(state.Results) != 2 {
		t.Fatalf("incomplete hosted run: %+v", state)
	}
	verifyHostedSnapshot(t, s, kind, history)
	verifyHostedRoles(t, state)
	metadata := hostedRead(t, filepath.Join(s.StorePath, "qual-platform.json"))
	var context platform.ReviewContext
	if err := json.Unmarshal(metadata, &context); err != nil {
		t.Fatal(err)
	}
	if context.Title != "Offline hosted review" || context.Head != history.head || context.TargetSHA != history.target {
		t.Fatalf("lost platform context: %+v", context)
	}
	var report domain.RunState
	if err := json.Unmarshal(hostedRead(t, filepath.Join(output, "report.json")), &report); err != nil {
		t.Fatal(err)
	}
	verifyHostedSnapshot(t, report.Snapshot, kind, history)
	if !strings.Contains(string(hostedRead(t, filepath.Join(output, "report.md"))), "changed hosted head") {
		t.Fatal("markdown report missing reviewed source")
	}
	if strings.Contains(out+diagnostic+string(metadata), "fixture-platform-token") {
		t.Fatal("platform credential leaked")
	}
}

func verifyHostedSnapshot(t *testing.T, s domain.Snapshot, kind string, h hostedHistory) {
	t.Helper()
	expected := map[string][2]string{
		"kind": {s.Kind, kind}, "original head": {s.OriginalHead, h.head}, "base": {s.BaseCommit, h.base},
		"target": {s.TargetCommit, h.target}, "rules": {s.RulesRevision, h.target}, "root": {s.Root, ""}, "head tree": {s.HeadTree, h.headTree},
	}
	for name, pair := range expected {
		if pair[0] != pair[1] {
			t.Errorf("%s: got %q, want %q", name, pair[0], pair[1])
		}
	}
	if s.HeadCommit == "" {
		t.Error("missing immutable head commit")
	}
}

func verifyHostedRoles(t *testing.T, state domain.RunState) {
	t.Helper()
	if state.Results[0].Role != domain.RoleReviewer || state.Results[1].Role != domain.RoleLeader {
		t.Fatalf("missing reviewer or leader: %+v", state.Results)
	}
	for _, result := range state.Results {
		if result.Status != domain.StatusCompleted || !strings.Contains(result.Report, "changed hosted head") {
			t.Fatalf("role did not read H and finish: %+v", result)
		}
	}
	for _, agent := range state.Agents {
		if !strings.Contains(agent.Instructions, "TARGET_RULE_FIXED_AT_T") || strings.Contains(agent.Instructions, "HEAD_RULE_MUST_NOT_APPEAR") || strings.Contains(agent.Instructions, "FOREIGN_RULE_MUST_NOT_APPEAR") {
			t.Fatalf("incorrect fixed rules for %s: %s", agent.ID, agent.Instructions)
		}
	}
}

func hostedRead(t *testing.T, path string) []byte {
	t.Helper()
	// #nosec G304 -- Paths originate from a test-owned CLI session and output directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func hostedEnvironment(t *testing.T, kind string, history hostedHistory) {
	t.Helper()
	cert, pemData := hostedCertificate(t)
	server := httptest.NewUnstartedServer(hostedMetadata(t, kind, history))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	proxy := hostedProxy(t, server.Listener.Addr().String())
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pemData, 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"HTTPS_PROXY": proxy.URL, "HTTP_PROXY": proxy.URL, "NO_PROXY": "127.0.0.1,localhost", "SSL_CERT_FILE": ca, "SSL_CERT_DIR": t.TempDir(), "QUAL_CI_CHECKOUT": history.repo, "QUAL_FIXTURE_API_KEY": "fixture-only-credential", "GITHUB_TOKEN": "fixture-platform-token", "GITLAB_TOKEN": "fixture-platform-token"} {
		t.Setenv(key, value)
	}
}

func hostedCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Offline hosted fixture"}, DNSNames: []string{"api.github.com", "gitlab.com", "gitlab.self-managed.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// The proxy can only tunnel to our TLS fixture; unexpected hosts never reach the network.
func hostedProxy(t *testing.T, address string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || (r.Host != "api.github.com:443" && r.Host != "gitlab.com:443" && r.Host != "gitlab.self-managed.test:443") {
			t.Errorf("unexpected proxy request: %s %s", r.Method, r.Host)
			http.Error(w, "fixture only", http.StatusForbidden)
			return
		}
		upstream, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", address)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if err := upstream.Close(); err != nil {
				t.Error(err)
			}
		}()
		hostedTunnel(t, w, upstream)
	}))
	t.Cleanup(server.Close)
	return server
}

func hostedCopy(t *testing.T, dst io.Writer, src io.Reader) {
	t.Helper()
	if _, err := io.Copy(dst, src); err != nil {
		t.Error(err)
	}
}

func hostedTunnel(t *testing.T, w http.ResponseWriter, upstream net.Conn) {
	t.Helper()
	var err error
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Error("proxy cannot hijack")
		return
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		t.Error(err)
		return
	}
	if err = buffer.Flush(); err != nil {
		t.Error(err)
		return
	}
	done := make(chan struct{})
	go func() {
		hostedCopy(t, upstream, buffer)
		if tcp, ok := upstream.(*net.TCPConn); ok {
			if err := tcp.CloseWrite(); err != nil {
				t.Error(err)
			}
		}
		close(done)
	}()
	hostedCopy(t, client, upstream)
	<-done
}

func hostedMetadata(t *testing.T, kind string, h hostedHistory) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if kind == "gitlab" {
			token = r.Header.Get("PRIVATE-TOKEN")
		} else {
			token = strings.TrimPrefix(token, "Bearer ")
		}
		if token != "fixture-platform-token" {
			t.Error("platform credential not routed to fixture")
		}
		w.Header().Set("Content-Type", "application/json")
		body := "[]"
		if kind == "github" {
			switch {
			case r.URL.Path == "/repos/owner/repo/pulls/7":
				body = fmt.Sprintf(`{"number":7,"title":"Offline hosted review","head":{"sha":%q,"ref":"feature","repo":{"full_name":"owner/repo","clone_url":"https://github.com/owner/repo.git"}},"base":{"sha":%q,"ref":"target","repo":{"full_name":"owner/repo","clone_url":"https://github.com/owner/repo.git"}}}`, h.head, h.target)
			case strings.HasSuffix(r.URL.Path, "/check-runs"):
				body = `{"check_runs":[]}`
			}
		} else {
			body = hostedGitLabMetadata(r.URL.Path, h)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Error(err)
		}
	})
}

func hostedGitLabMetadata(path string, h hostedHistory) string {
	switch path {
	case "/api/v4/projects/owner/repo":
		return `{"id":10,"path_with_namespace":"owner/repo","http_url_to_repo":"https://gitlab.com/owner/repo.git"}`
	case "/api/v4/projects/owner/repo/merge_requests/7":
		return fmt.Sprintf(`{"iid":7,"project_id":10,"source_project_id":10,"title":"Offline hosted review","sha":%q,"source_branch":"feature","target_branch":"target"}`, h.head)
	case "/api/v4/projects/owner/repo/repository/branches/target":
		return fmt.Sprintf(`{"commit":{"id":%q}}`, h.target)
	default:
		return "[]"
	}
}

func TestExecutableSelfManagedGitLabConfiguredThroughInit(t *testing.T) {
	binary := buildCLI(t)
	history := hostedRepository(t)
	requests := selfManagedHostedEnvironment(t, history)
	provider, calls := fixture(t)
	defer provider.Close()
	home, cwd := t.TempDir(), t.TempDir()
	configure(t, home, provider.URL+"/v1")
	settings := hostedRead(t, filepath.Join(home, "settings.yaml"))
	target := "https://gitlab.self-managed.test/owner/repo/-/merge_requests/7"
	_, diagnostic, err := invoke(t, binary, home, cwd, "", "review", target, "--json", "--non-interactive")
	if err == nil || !strings.Contains(diagnostic, "qual init --gitlab-host gitlab.self-managed.test") || requests.Load() != 0 {
		t.Fatalf("unknown host: %v %s", err, diagnostic)
	}
	out, diagnostic, err := invoke(t, binary, home, cwd, "", "init", "--gitlab-host", "gitlab.self-managed.test", "--gitlab-token-env", "QUAL_CUSTOM_GITLAB_TOKEN", "--accept", "--non-interactive", "--json")
	if err != nil || !strings.Contains(out, `"accepted":true`) {
		t.Fatalf("host init: %v %s %s", err, out, diagnostic)
	}
	if string(settings) != string(hostedRead(t, filepath.Join(home, "settings.yaml"))) || calls.Load() != 0 {
		t.Fatal("host-only init changed model defaults or invoked inference")
	}
	registry := hostedRead(t, filepath.Join(home, "platform-hosts.json"))
	if strings.Contains(string(registry), "custom-host-secret") || strings.Contains(out+diagnostic, "custom-host-secret") {
		t.Fatal("host token leaked")
	}
	verifySelfManagedReview(t, binary, home, cwd, target, requests)
}

func verifySelfManagedReview(t *testing.T, binary, home, cwd, target string, requests *atomic.Int64) {
	t.Helper()
	out, diagnostic, err := invoke(t, binary, home, cwd, "", "review", target, "--json", "--non-interactive", "--output", t.TempDir())
	if err != nil {
		t.Fatalf("custom host review: %v %s %s", err, out, diagnostic)
	}
	var state domain.RunState
	if err = json.Unmarshal([]byte(out), &state); err != nil || state.Status != domain.StatusCompleted || state.Snapshot.Repository != "https://gitlab.self-managed.test/owner/repo" {
		t.Fatalf("custom snapshot: %v %s", err, out)
	}
	if requests.Load() == 0 {
		t.Fatal("custom host was not contacted")
	}
	_, diagnostic, err = invoke(t, binary, home, cwd, "", "review", "https://gitlab.com/owner/repo/-/merge_requests/7", "--json", "--non-interactive")
	if err != nil {
		t.Fatalf("public host credential isolation: %v %s", err, diagnostic)
	}
	verifyHostedRoles(t, state)
}

func selfManagedHostedEnvironment(t *testing.T, history hostedHistory) *atomic.Int64 {
	t.Helper()
	requests := &atomic.Int64{}
	cert, pemData := hostedCertificate(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := "gitlab.com"
		expected := "public-host-secret"
		if r.Host == "gitlab.self-managed.test" {
			host = "gitlab.self-managed.test"
			expected = "custom-host-secret"
			requests.Add(1)
		}
		if r.Header.Get("PRIVATE-TOKEN") != expected || r.Header.Get("Authorization") != "" {
			t.Error("host-specific GitLab authentication was not isolated")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body := strings.ReplaceAll(hostedGitLabMetadata(r.URL.Path, history), "gitlab.com", host)
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, body); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	proxy := hostedProxy(t, server.Listener.Addr().String())
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pemData, 0o600); err != nil {
		t.Fatal(err)
	}
	// #nosec G101 -- Nonfunctional tokens belong only to the local TLS authentication-isolation fixture.
	for key, value := range map[string]string{"HTTPS_PROXY": proxy.URL, "HTTP_PROXY": proxy.URL, "NO_PROXY": "127.0.0.1,localhost", "SSL_CERT_FILE": ca, "SSL_CERT_DIR": t.TempDir(), "QUAL_CI_CHECKOUT": history.repo, "QUAL_FIXTURE_API_KEY": "fixture-only-credential", "GITLAB_TOKEN": "public-host-secret", "QUAL_CUSTOM_GITLAB_TOKEN": "custom-host-secret"} {
		t.Setenv(key, value)
	}
	return requests
}

func TestTerminalInitProtectsSelfManagedGitLabToken(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	p := startTerminal(t, binary, home, t.TempDir(), 80, "init")
	p.expect(t, "How would you like to connect?")
	p.send(t, strings.Repeat("\x1b[B", 7)+"\r")
	p.expect(t, "GitLab hostname")
	p.send(t, "gitlab.self-managed.test\r")
	p.expect(t, "GitLab access token")
	p.send(t, "private-host-token\r")
	p.expect(t, "Trusted GitLab repository access")
	p.send(t, "\r")
	p.expect(t, "Trust this GitLab host")
	p.send(t, "\r")
	p.wait(t, true)
	if strings.Contains(p.output.text(), "private-host-token") {
		t.Fatal("hidden GitLab token leaked")
	}
	registry := hostedRead(t, filepath.Join(home, "platform-hosts.json"))
	if strings.Contains(string(registry), "private-host-token") || !strings.Contains(string(registry), "profile:gitlab_") {
		t.Fatal("token not stored as a protected reference")
	}
	var hosts struct {
		GitLab map[string]struct {
			Credential string `json:"credential"`
		} `json:"gitlab_hosts"`
	}
	if err := json.Unmarshal(registry, &hosts); err != nil {
		t.Fatal(err)
	}
	reference := hosts.GitLab["gitlab.self-managed.test"].Credential
	path := filepath.Join(home, "auth", strings.TrimPrefix(reference, "profile:")+".json")
	if !strings.Contains(string(hostedRead(t, path)), "private-host-token") {
		t.Fatal("protected host token not saved")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file permissions: %v %v", info, err)
	}
	if _, err = os.Stat(filepath.Join(home, "settings.yaml")); !os.IsNotExist(err) {
		t.Fatalf("repository access setup changed model settings: %v", err)
	}
}

func TestExecutableGitLabInitRejectsIncompleteInputWithoutReadingStdin(t *testing.T) {
	binary := buildCLI(t)
	for _, args := range [][]string{
		{"init", "--kind", "gitlab-host", "--json"},
		{"init", "--kind", "gitlab-host", "--non-interactive"},
		{"init", "--gitlab-token-env", "CUSTOM_TOKEN", "--non-interactive"},
		{"init", "--gitlab-host", "gitlab.self-managed.test", "--kind", "openai", "--accept", "--non-interactive"},
	} {
		verifyGitLabHostInputError(t, binary, args)
	}
}

func verifyGitLabHostInputError(t *testing.T, binary string, args []string) {
	t.Helper()
	input, hold, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := input.Close(); e != nil {
			t.Log(e)
		}
		if e := hold.Close(); e != nil {
			t.Log(e)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	// #nosec G204 -- Fixed test-built CLI, intentionally incomplete flags and held-open stdin.
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append(os.Environ(), "QUAL_HOME="+t.TempDir(), "QUAL_AUTH_BACKEND=file")
	command.Stdin = input
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("GitLab init waited for unavailable input: %s", output)
	}
	if err == nil || !strings.Contains(string(output), "qual init --gitlab-host") || strings.Contains(string(output), "panic") {
		t.Fatalf("missing actionable host guidance: %v %s", err, output)
	}
}
