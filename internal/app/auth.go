package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamnalinor/qual/internal/auth"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/tui"
)

func (a *App) authService() (*auth.Service, error) {
	backend, err := a.authBackend()
	if err != nil {
		return nil, err
	}
	selected := auth.Config{Home: a.Home}
	if backend == "keyring" {
		selected.Keyring = systemKeyring{}
	}
	return auth.New(selected), nil
}

func (a *App) authBackend() (string, error) {
	path := filepath.Join(a.Home, "auth-backend")
	data, err := os.ReadFile(path) //nolint:gosec // Fixed file under explicitly selected application home; contains backend name only.
	if err == nil {
		value := strings.TrimSpace(string(data))
		if value != "file" && value != "keyring" {
			return "", invalid("invalid saved authentication backend")
		}
		return value, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	backend := selectBackend()
	if backend != "file" && backend != "keyring" {
		return "", invalid("QUAL_AUTH_BACKEND must be file or keyring")
	}
	if err = os.MkdirAll(a.Home, 0o700); err != nil {
		return "", err
	}
	if err = writePrivate(path, []byte(backend+"\n")); err != nil {
		return "", err
	}
	return backend, nil
}

// systemKeyring uses libsecret's maintained Secret Service client. No secret enters argv.
type systemKeyring struct{}

func keyringCommand(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// #nosec G204 -- Fixed libsecret executable with adapter-owned argument arrays; secrets use stdin only.
	cmd := exec.CommandContext(ctx, "secret-tool", args...)
	cmd.Stdin = strings.NewReader(string(input))
	out, err := cmd.Output()
	if err != nil {
		return nil, &domain.Error{Code: domain.ErrAuth, Message: "selected OS keyring operation failed"}
	}
	return out, nil
}

func (systemKeyring) Get(ctx context.Context, id string) ([]byte, error) {
	return keyringCommand(ctx, nil, "lookup", "service", "qual", "profile", id)
}

func (systemKeyring) Set(ctx context.Context, id string, data []byte) error {
	_, err := keyringCommand(ctx, data, "store", "--label=Qual credential", "service", "qual", "profile", id)
	return err
}

func (systemKeyring) Delete(ctx context.Context, id string) error {
	_, err := keyringCommand(ctx, nil, "clear", "service", "qual", "profile", id)
	return err
}

func (systemKeyring) List(ctx context.Context) ([]string, error) {
	data, err := keyringCommand(ctx, nil, "search", "--all", "service", "qual")
	if err != nil {
		return nil, err
	}
	var result []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "attribute.profile = "); ok {
			result = append(result, value)
		}
	}
	return result, nil
}

// LoginOptions contains secret input selectors, never plaintext secret arguments.
type LoginOptions struct {
	Kind, Profile, Env string
	Stdin, Device      bool
}

// Login saves an explicitly selected credential route.
func (a *App) Login(ctx context.Context, options LoginOptions) error {
	if a.terminal == nil && (options.Kind == "chatgpt" || options.Kind == "codex" || (!options.Stdin && options.Env == "")) {
		return invalid("Run qual init to connect your account and choose models. Advanced credential import requires --kind KIND --profile NAME and --stdin or --key-env NAME.")
	}
	return a.login(ctx, options)
}

func (a *App) login(ctx context.Context, options LoginOptions) error {
	var err error
	options, err = a.loginChoices(ctx, options)
	if err != nil {
		return err
	}
	if options.Kind == "compatible" {
		options.Kind = "openai-compatible"
	}
	if err := validateLoginKind(options.Kind); err != nil {
		return err
	}
	service, err := a.authService()
	if err != nil {
		return err
	}
	_, err = a.connectAccount(ctx, service, options)
	return err
}

func (a *App) connectAccount(ctx context.Context, service *auth.Service, options LoginOptions) (string, error) {
	record, err := a.authenticate(ctx, service, options)
	if err != nil {
		return "", err
	}
	if err = a.confirmation("Account connected", record.ID+" ("+record.Kind+")", map[string]string{"profile": record.ID, "kind": record.Kind}); err != nil {
		return "", err
	}
	return record.ID, nil
}

func (a *App) authenticate(ctx context.Context, service *auth.Service, options LoginOptions) (auth.Credentials, error) {
	if oauthKind(options.Kind) {
		return a.oauthCredentials(ctx, service, options)
	}
	if options.Profile == "" {
		return auth.Credentials{}, invalid("Run qual init to connect your account and choose models. Advanced scripted login accepts --kind KIND --profile NAME with --stdin or --key-env NAME.")
	}
	key, err := a.readSecret(ctx, options)
	if err != nil {
		return auth.Credentials{}, err
	}
	record := auth.Credentials{ID: options.Profile, Kind: options.Kind, AccessToken: key}
	if err = service.Save(ctx, record); err != nil {
		return auth.Credentials{}, err
	}
	return record, nil
}

func (a *App) readSecret(ctx context.Context, options LoginOptions) (string, error) {
	if options.Env != "" {
		value := os.Getenv(options.Env)
		if value == "" {
			return "", &domain.Error{Code: domain.ErrAuth, Message: "selected key environment variable is empty"}
		}
		return value, nil
	}
	if options.Stdin {
		data, err := readInput(ctx, a.In)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(string(data))
		if value == "" {
			return "", invalid("empty API key")
		}
		return value, nil
	}
	if a.terminal != nil {
		return a.terminalSecret(ctx)
	}
	if a.NonInteractive {
		return "", invalid("noninteractive login requires --stdin or --key-env")
	}
	file, ok := a.In.(*os.File)
	if !ok {
		return "", invalid("secret prompt requires a terminal or --stdin")
	}
	value, err := a.hiddenSecret(ctx, file)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", invalid("empty API key")
	}
	return value, nil
}

func (a *App) oauthCredentials(ctx context.Context, service *auth.Service, o LoginOptions) (auth.Credentials, error) {
	if a.NonInteractive {
		return auth.Credentials{}, invalid("OAuth login requires interactive authorization")
	}
	var record auth.Credentials
	var err error
	if o.Device {
		if o.Kind != "codex" {
			return auth.Credentials{}, invalid("device login is supported only by the explicit codex route")
		}
		record, err = service.DeviceLogin(ctx, func(code auth.DeviceCode) error {
			return a.authorization(code.VerificationURL, code.UserCode)
		})
	} else {
		record, err = service.BrowserLogin(ctx, auth.LoginOptions{Kind: o.Kind, Profile: o.Profile}, func(endpoint string) error {
			return a.authorization(endpoint, "")
		})
	}
	if err != nil {
		return auth.Credentials{}, err
	}
	return record, nil
}

// AuthList returns safe credential metadata only.
func (a *App) AuthList(ctx context.Context) error {
	if err := a.globalUI(ctx); err != nil {
		return err
	}
	service, err := a.authService()
	if err != nil {
		return err
	}
	profiles, err := service.List(ctx)
	if err != nil {
		return err
	}
	return a.renderProfiles(profiles)
}

// Logout deletes only the selected credential profile.
func (a *App) Logout(ctx context.Context, id string) error {
	service, err := a.authService()
	if err != nil {
		return err
	}
	if err = service.Logout(ctx, id); err != nil {
		return err
	}
	return a.confirmation("Credential profile removed", id, map[string]string{"removed": id})
}

func writePrivate(path string, data []byte) (retErr error) {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return invalid("configuration destination must be a regular file")
	}
	file, err := os.CreateTemp(parent, ".qual-write-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() {
		if e := os.Remove(name); e != nil && !errors.Is(e, os.ErrNotExist) {
			retErr = errors.Join(retErr, e)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func selectBackend() string {
	if selected := os.Getenv("QUAL_AUTH_BACKEND"); selected != "" {
		return selected
	}
	if _, err := exec.LookPath("secret-tool"); err == nil && os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return "keyring"
	}
	return "file"
}

func validateLoginKind(kind string) error {
	switch kind {
	case "openai", "chatgpt", "codex", "anthropic", "gemini", "openrouter", "openai-compatible", "compatible":
		return nil
	default:
		return invalid("unknown authentication provider kind")
	}
}

func readInput(ctx context.Context, reader io.Reader) ([]byte, error) {
	type input struct {
		data []byte
		err  error
	}
	result := make(chan input, 1)
	go func() { data, err := io.ReadAll(io.LimitReader(reader, 64<<10)); result <- input{data, err} }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-result:
		return value.data, value.err
	}
}

func readLine(ctx context.Context, reader io.Reader) (string, error) {
	type input struct {
		text string
		err  error
	}
	result := make(chan input, 1)
	go func() { text, err := bufio.NewReader(reader).ReadString('\n'); result <- input{text, err} }()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value := <-result:
		return value.text, value.err
	}
}

func (a *App) authorization(endpoint, code string) error {
	if a.terminal != nil {
		message := a.text("Open this address in your browser:") + " " + endpoint
		if code != "" {
			message += " · " + a.text("Authorization code") + ": " + code
		}
		a.terminal.Activity(tui.Activity{Summary: message})
		return nil
	}
	if a.JSON {
		return json.NewEncoder(a.Err).Encode(struct {
			SchemaVersion int    `json:"schema_version"`
			Type          string `json:"type"`
			URL           string `json:"url"`
			Code          string `json:"code,omitempty"`
		}{1, "authorization", endpoint, code})
	}
	if code != "" {
		_, err := fmt.Fprintf(a.Err, "Authorize at %s with code %s\n", endpoint, code)
		return err
	}
	_, err := fmt.Fprintf(a.Err, "Open this authorization URL in your browser: %s\n", endpoint)
	return err
}

func (a *App) loginChoices(ctx context.Context, options LoginOptions) (LoginOptions, error) {
	if options.Kind == "" {
		options.Kind = "openai"
		if a.terminal != nil {
			var err error
			options.Kind, err = a.providerChoice(ctx)
			if err != nil {
				return options, err
			}
		}
	}
	if options.Profile == "" && a.terminal != nil && !oauthKind(options.Kind) {
		service, err := a.authService()
		if err != nil {
			return options, err
		}
		profiles, err := service.List(ctx)
		if err != nil {
			return options, err
		}
		options.Profile = uniqueProfile(options.Kind, profiles)
	}
	return options, nil
}

func (a *App) terminalSecret(ctx context.Context) (string, error) {
	value, err := a.terminal.Ask(ctx, tui.Prompt{Title: a.text("API key"), Description: a.text("Paste your key. It stays hidden and is saved in your protected credential store."), Secret: true})
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", invalid("API key is empty; paste a key or press Esc to cancel.")
	}
	return value, nil
}

func oauthKind(kind string) bool { return kind == "chatgpt" || kind == "codex" }
