package app

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/iamnalinor/qual/internal/config"
)

type environmentInfo struct {
	SchemaVersion   int    `json:"schema_version"`
	Type            string `json:"type"`
	ExternalSandbox string `json:"external_sandbox"`
	Network         string `json:"network,omitempty"`
	Message         string `json:"message"`
}

// The marker describes the launcher report. It never authorizes any capability.
func executionEnvironment() environmentInfo {
	info := environmentInfo{SchemaVersion: 1, Type: "execution_environment", ExternalSandbox: "not reported"}
	if os.Getenv("QUAL_SANDBOX_ACTIVE") == "1" {
		info.ExternalSandbox = "launcher-reported active"
		switch network := os.Getenv("QUAL_SANDBOX_NETWORK"); network {
		case "none", "restricted", "open":
			info.Network = network
		}
	}
	return info
}

func (a *App) announceEnvironment() error {
	info := executionEnvironment()
	info.Message = a.text("External sandbox")
	if a.JSON {
		return json.NewEncoder(a.Err).Encode(info)
	}
	_, err := fmt.Fprintf(a.Err, "%s: %s; network: %s\n", a.text("External sandbox"), a.text(info.ExternalSandbox), info.Network)
	return err
}

func (a *App) announceIfPlain() error {
	if a.interactive() {
		return nil
	}
	return a.announceEnvironment()
}

func (a *App) prepareUI(e config.Effective) error {
	if err := a.selectUI(e); err != nil {
		return err
	}
	return a.announceIfPlain()
}
