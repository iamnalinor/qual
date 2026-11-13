package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/iamnalinor/qual/internal/domain"
)

type dependencyCheck struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	OnPath    bool   `json:"on_path"`
	Path      string `json:"path,omitempty"`
	Status    string `json:"status"`
	Action    string `json:"action,omitempty"`
	Required  bool   `json:"required"`
}
type doctorReport struct {
	SchemaVersion int               `json:"schema_version"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	Home          string            `json:"home"`
	Checks        []dependencyCheck `json:"checks"`
	Sandbox       struct {
		Available        bool   `json:"available"`
		BackendAvailable bool   `json:"backend_available"`
		ReportedActive   bool   `json:"reported_active"`
		Network          string `json:"network,omitempty"`
	} `json:"sandbox"`
	ConnectionsChecked bool `json:"connections_checked"`
}

// Doctor checks real dependencies, distinguishing installed sandbox support from launcher activation.
func (a *App) Doctor(ctx context.Context, connections bool) error {
	if err := a.globalUI(ctx); err != nil {
		return err
	}
	report := doctorReport{SchemaVersion: 1, OS: runtime.GOOS, Arch: runtime.GOARCH, Home: a.Home, ConnectionsChecked: connections}
	git := dependency("Git", "git", true, "Install Git and retry.")
	launcher := sandboxLauncher()
	backend := dependency("Bubblewrap", "bwrap", false, "Install bubblewrap to use the Linux external sandbox.")
	report.Checks = []dependencyCheck{git, launcher, backend}
	report.Sandbox.Available = launcher.Available
	report.Sandbox.BackendAvailable = backend.Available
	info := executionEnvironment()
	report.Sandbox.ReportedActive = info.ExternalSandbox == "launcher-reported active"
	report.Sandbox.Network = info.Network
	if connections {
		checks, err := a.connectionChecks(ctx)
		if err != nil {
			return err
		}
		report.Checks = append(report.Checks, checks...)
	}
	if err := a.renderDoctor(report); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if check.Required && !check.Available {
			return &domain.Error{Code: domain.ErrUnsupportedCapability, Message: "required dependency or connection unavailable"}
		}
	}
	return nil
}

func dependency(name, executable string, required bool, action string) dependencyCheck {
	path, err := exec.LookPath(executable)
	check := dependencyCheck{Name: name, Available: err == nil, OnPath: err == nil, Path: path, Required: required, Status: "ready"}
	if err != nil {
		check.Status = "missing"
		check.Action = action
	}
	return check
}

func sandboxLauncher() dependencyCheck {
	check := dependency("Sandbox launcher", "qual-sandbox", false, "Install qual-sandbox or use scripts/qual-sandbox from your trusted installation.")
	if check.Available {
		return check
	}
	executable, err := os.Executable()
	if err != nil {
		return check
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return check
	}
	directory := filepath.Dir(executable)
	if filepath.Base(directory) != "bin" {
		return check
	}
	candidate := filepath.Join(filepath.Dir(directory), "scripts", "qual-sandbox")
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return check
	}
	check.Available = true
	check.Path = candidate
	check.Status = "available beside executable (not on PATH)"
	check.Action = "Invoke the launcher path above directly, or add its scripts directory to PATH."
	return check
}

func (a *App) connectionChecks(ctx context.Context) ([]dependencyCheck, error) {
	e, err := a.Config(ctx, "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(e.Settings.Connections))
	for name := range e.Settings.Connections {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []dependencyCheck{{Name: "Provider accounts", Status: "not configured", Action: "Run qual init to connect an account and choose models."}}, nil
	}
	checks := make([]dependencyCheck, 0, len(names))
	for _, name := range names {
		err = a.checkConnection(ctx, e.Settings.Connections[name])
		check := dependencyCheck{Name: "Connection: " + name, Required: true, Available: err == nil, Status: "ready"}
		if err != nil {
			check.Status = "unavailable"
			check.Action = "Run qual init to reconnect this account. " + err.Error()
		}
		checks = append(checks, check)
	}
	return checks, nil
}
