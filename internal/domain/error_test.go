package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorPreservesCodeAndCause(t *testing.T) {
	cause := errors.New("underlying failure")
	err := &Error{Code: ErrTool, Message: "run tool", Cause: cause}
	if !errors.Is(err, cause) {
		t.Fatal("cause was lost")
	}
	if got := CodeOf(fmt.Errorf("outer: %w", err)); got != ErrTool {
		t.Fatalf("code = %q", got)
	}
	if got := err.Error(); got != "tool: run tool: underlying failure" {
		t.Fatalf("message = %q", got)
	}
	if got := CodeOf(cause); got != "" {
		t.Fatalf("uncoded error code = %q", got)
	}
	if got := CodeOf(nil); got != "" {
		t.Fatalf("nil error code = %q", got)
	}
}
