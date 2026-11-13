package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/iamnalinor/qual/internal/app"
	"github.com/iamnalinor/qual/internal/citemplate"
	"github.com/spf13/cobra"
)

func (s *shell) ci() *cobra.Command {
	cmd := &cobra.Command{Use: "ci", Short: "Generate trusted pipelines and authorize review events"}
	cmd.AddCommand(s.ciGenerate(), s.ciAuthorize())
	return cmd
}

func (s *shell) ciGenerate() *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "generate <github|gitlab>", Short: "Emit workflow YAML and required installation notes", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return s.run(cmd, func(a *app.App) error {
			files, err := citemplate.Generate(args[0])
			if err != nil {
				return err
			}
			if output != "" {
				return saveCI(a, output, args[0], files)
			}
			if a.JSON {
				return a.Output(files)
			}
			if _, err = fmt.Fprint(a.Err, files.Setup); err != nil {
				return err
			}
			_, err = fmt.Fprint(a.Out, files.Workflow)
			return err
		})
	}}
	cmd.Flags().StringVar(&output, "output", "", "Write generated files into this directory")
	return cmd
}

func (s *shell) ciAuthorize() *cobra.Command {
	in := citemplate.Input{}
	var eventFile string
	cmd := &cobra.Command{Use: "authorize", Short: "Authorize trusted event context and pin current source commits", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return s.run(cmd, func(a *app.App) error {
			data, err := readEvent(eventFile)
			if err != nil {
				return err
			}
			in.Payload = data
			switch in.Provider {
			case "github":
				in.Token = os.Getenv("GITHUB_TOKEN")
			case "gitlab":
				in.Token = os.Getenv("GITLAB_TOKEN")
			}
			client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			decision, err := citemplate.Authorize(cmd.Context(), in, client)
			if err != nil {
				return err
			}
			return a.Output(decision)
		})
	}}
	f := cmd.Flags()
	f.StringVar(&in.Provider, "provider", "", "github or gitlab")
	f.StringVar(&in.Event, "event", "", "Trusted runner event name")
	f.StringVar(&eventFile, "event-file", "", "Untrusted event JSON file")
	f.StringVar(&in.Repository, "repository", "", "Trusted repository identity")
	f.StringVar(&in.APIURL, "api-url", "", "Trusted platform API base URL")
	f.StringVar(&in.Actor, "actor", "", "Trusted manual dispatch actor")
	f.IntVar(&in.ManualNumber, "number", 0, "Trusted manual review number")
	f.BoolVar(&in.TrustedSource, "trusted-source", false, "Caller asserts workflow and Qual source are trusted")
	return cmd
}

func saveCI(a *app.App, output, provider string, files citemplate.Files) error {
	if err := os.MkdirAll(output, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, provider+".yml"), []byte(files.Workflow), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "SETUP.md"), []byte(files.Setup), 0o600); err != nil {
		return err
	}
	return a.FilePaths(map[string]string{"workflow": filepath.Join(output, provider+".yml"), "setup": filepath.Join(output, "SETUP.md")})
}

func readEvent(path string) ([]byte, error) {
	// #nosec G304 -- This command intentionally reads the explicitly selected event payload as untrusted bounded data.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 2<<20+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("ci_invalid: event file too large")
	}
	return data, nil
}
