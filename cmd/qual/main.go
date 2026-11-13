// Command qual runs the Qual review CLI.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/iamnalinor/qual/internal/cli"
	"github.com/iamnalinor/qual/internal/domain"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	command := cli.NewRootCommand()
	if err := command.ExecuteContext(ctx); err != nil {
		if writeErr := diagnostic(os.Stderr, err, command.Flag("json").Value.String() == "true"); writeErr != nil {
			return 1
		}
		return 1
	}
	return 0
}

func diagnostic(writer io.Writer, err error, structured bool) error {
	if structured {
		return json.NewEncoder(writer).Encode(map[string]any{"schema_version": 1, "type": "error", "code": domain.CodeOf(err), "message": err.Error()})
	}
	_, writeErr := fmt.Fprintln(writer, humanError(err, 0))
	return writeErr
}

// humanError removes machine labels while retaining operation context and safe causes.
// Transform only text already exposed by Error(), so redacted wrappers stay redacted.
func humanError(err error, depth int) string {
	if depth > 32 {
		return err.Error()
	}
	switch value := err.(type) { //nolint:errorlint // Format each direct layer without skipping operation wrappers.
	case *domain.Error:
		message := value.Message
		if value.Cause != nil {
			message += ": " + humanError(value.Cause, depth+1)
		}
		return message
	case interface{ Unwrap() []error }:
		message := err.Error()
		for _, child := range value.Unwrap() {
			if child != nil {
				message = strings.Replace(message, child.Error(), humanError(child, depth+1), 1)
			}
		}
		return message
	case interface{ Unwrap() error }:
		child := value.Unwrap()
		if child != nil {
			if prefix, ok := strings.CutSuffix(err.Error(), child.Error()); ok {
				return prefix + humanError(child, depth+1)
			}
		}
	}
	return err.Error()
}
