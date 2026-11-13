package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelpAndUnknownCommand(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"not-a-command"}} {
		command := NewRootCommand()
		var out bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&out)
		command.SetArgs(args)
		err := command.Execute()
		if args[0] == "--help" {
			if err != nil || !strings.Contains(out.String(), "Qual") {
				t.Fatalf("help = %q, %v", out.String(), err)
			}
		} else if err == nil {
			t.Fatal("unknown command succeeded")
		}
	}
}
