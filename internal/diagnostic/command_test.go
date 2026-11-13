package diagnostic

import (
	"errors"
	"strings"
	"testing"
)

func TestGitCauseBoundsAndRedactsEnvironmentSecrets(t *testing.T) {
	original := errors.New("exit status 128")
	cause := GitCause(original, "fatal: synthetic-token https://user:password@host.invalid/private\n\x1b[31m"+strings.Repeat("x", 5000), []string{"GIT_CONFIG_VALUE_0=synthetic-token"})
	if !errors.Is(cause, original) || !strings.Contains(cause.Error(), "fatal:") || len(cause.Error()) > 4096 {
		t.Fatalf("lost cause or bound: %.100s (%d bytes)", cause, len(cause.Error()))
	}
	for _, secret := range []string{"synthetic-token", "password", "\n", "\x1b"} {
		if strings.Contains(cause.Error(), secret) {
			t.Fatalf("unsafe cause: %.100s", cause)
		}
	}
}

func TestGitCauseRedactsAuthorizationCredential(t *testing.T) {
	cause := GitCause(errors.New("exit status 128"), "fatal: rejected synthetic-base64", []string{"GIT_CONFIG_VALUE_0=Authorization: Basic synthetic-base64"})
	if strings.Contains(cause.Error(), "synthetic-base64") {
		t.Fatal(cause)
	}
}
