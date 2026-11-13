package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/iamnalinor/qual/internal/gitstore"
)

const automaticRetention = 14 * 24 * time.Hour

// AutomaticCleanup collects old disposable worktrees without masking the requested command.
// A returned error means its warning could not be written, not that collection failed.
func (a *App) AutomaticCleanup(ctx context.Context) error {
	registry := filepath.Join(a.Home, "worktrees")
	_, err := os.Stat(registry)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil {
		_, err = gitstore.New(a.Home).Cleanup(ctx, time.Now(), automaticRetention)
	}
	if err == nil {
		return nil
	}
	message := a.text("Automatic cleanup did not complete. Run qual cleanup for details; the requested command will continue.")
	if a.JSON {
		return json.NewEncoder(a.Err).Encode(map[string]any{"schema_version": 1, "type": "warning", "code": "automatic_cleanup_failed", "message": message})
	}
	_, writeErr := a.Err.Write([]byte(a.text("Warning") + ": " + message + "\n"))
	return writeErr
}
