// Package citemplate provides trusted CI templates and event authorization.
package citemplate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// HTTPDoer permits bounded API calls and offline HTTP fixtures.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Input contains trusted runner context and untrusted webhook data. Token is a
// read-only platform credential; model credentials must only enter after approval.
type Input struct {
	Provider      string
	Event         string
	Payload       []byte
	Repository    string
	APIURL        string
	Token         string
	TrustedSource bool
	// ManualNumber and Actor must come from the trusted CI dispatch context.
	ManualNumber int
	Actor        string
}

// Decision pins the platform identity and immutable commits approved for review.
type Decision struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
	Actor      string `json:"actor"`
	TargetSHA  string `json:"target_sha"`
	HeadSHA    string `json:"head_sha"`
}

var (
	shaPattern   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	actorPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// Authorize resolves live metadata and initiator permissions before any model
// credential is made available. API requests inherit cancellation and a deadline.
func Authorize(ctx context.Context, in Input, client HTTPDoer) (Decision, error) {
	if !in.TrustedSource {
		return Decision{}, fmt.Errorf("ci_untrusted: workflow and tool source must be trusted")
	}
	if client == nil {
		return Decision{}, fmt.Errorf("ci_invalid: HTTP client is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch in.Provider {
	case "github":
		return authorizeGitHub(ctx, in, client)
	case "gitlab":
		return authorizeGitLab(ctx, in, client)
	default:
		return Decision{}, fmt.Errorf("ci_unsupported: unknown provider %q", in.Provider)
	}
}

func apiGet(ctx context.Context, in Input, client HTTPDoer, path string, out any) (resultErr error) {
	if err := validateAPIURL(in.APIURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(in.APIURL, "/")+path, nil)
	if err != nil {
		return fmt.Errorf("ci_api: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+in.Token)
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ci_api: request failed: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("ci_api: close response: %w", err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ci_api: unexpected HTTP status %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(out); err != nil {
		return fmt.Errorf("ci_api: decode response: %w", err)
	}
	return nil
}

type githubEvent struct {
	Action     string
	Number     int
	Repository struct {
		FullName string `json:"full_name"`
	}
	Sender struct{ Login string }
	Label  struct{ Name string }
	Issue  struct {
		Number      int
		PullRequest *json.RawMessage `json:"pull_request"`
	}
	Comment struct{ Body string }
}

func githubTrigger(in Input) (int, string, error) {
	var event githubEvent
	if err := json.Unmarshal(in.Payload, &event); err != nil {
		return 0, "", fmt.Errorf("ci_event: decode GitHub event: %w", err)
	}
	if event.Repository.FullName != in.Repository {
		return 0, "", fmt.Errorf("ci_event: repository mismatch")
	}
	switch in.Event {
	case "pull_request_target":
		if !githubAction(event) {
			return 0, "", fmt.Errorf("ci_skipped: unsupported pull request action")
		}
		return event.Number, event.Sender.Login, nil
	case "issue_comment":
		if event.Action != "created" || event.Comment.Body != "/qual review" || event.Issue.PullRequest == nil {
			return 0, "", fmt.Errorf("ci_skipped: unsupported comment")
		}
		return event.Issue.Number, event.Sender.Login, nil
	case "workflow_dispatch":
		return in.ManualNumber, in.Actor, nil
	default:
		return 0, "", fmt.Errorf("ci_event: unsupported GitHub event")
	}
}

func authorizeGitHub(ctx context.Context, in Input, client HTTPDoer) (Decision, error) {
	if !repoPattern.MatchString(in.Repository) {
		return Decision{}, fmt.Errorf("ci_invalid: invalid GitHub repository")
	}
	number, actor, err := githubTrigger(in)
	if err != nil {
		return Decision{}, err
	}
	if number <= 0 || !actorPattern.MatchString(actor) {
		return Decision{}, fmt.Errorf("ci_event: invalid pull request or actor")
	}
	var pr githubPR
	path := "/repos/" + in.Repository
	if err := apiGet(ctx, in, client, fmt.Sprintf("%s/pulls/%d", path, number), &pr); err != nil {
		return Decision{}, err
	}
	if !pr.valid(in.Repository, number) {
		return Decision{}, fmt.Errorf("ci_skipped: pull request is draft, closed, or mismatched")
	}
	var permission struct{ Permission string }
	if err := apiGet(ctx, in, client, path+"/collaborators/"+url.PathEscape(actor)+"/permission", &permission); err != nil {
		return Decision{}, err
	}
	if !slices.Contains([]string{"write", "maintain", "admin"}, permission.Permission) {
		return Decision{}, fmt.Errorf("ci_denied: initiator requires write permission")
	}
	return checkedDecision(Decision{Provider: in.Provider, Repository: in.Repository, Number: number, URL: pr.URL, Actor: actor, TargetSHA: pr.Base.SHA, HeadSHA: pr.Head.SHA})
}

func checkedDecision(d Decision) (Decision, error) {
	if !shaPattern.MatchString(d.TargetSHA) || !shaPattern.MatchString(d.HeadSHA) {
		return Decision{}, fmt.Errorf("ci_snapshot: missing or invalid commit SHA")
	}
	parsed, err := url.Parse(d.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return Decision{}, fmt.Errorf("ci_snapshot: invalid review URL")
	}
	return d, nil
}

func validateAPIURL(raw string) error {
	base, err := url.Parse(raw)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("ci_invalid: invalid trusted API URL")
	}
	localHTTP := base.Scheme == "http" && slices.Contains([]string{"localhost", "127.0.0.1"}, base.Hostname())
	if base.Scheme != "https" && !localHTTP {
		return fmt.Errorf("ci_invalid: API requires HTTPS")
	}
	return nil
}

func githubAction(event githubEvent) bool {
	return slices.Contains([]string{"opened", "reopened", "ready_for_review", "synchronize"}, event.Action) || (event.Action == "labeled" && event.Label.Name == "qual-review")
}

type githubPR struct {
	Number int
	State  string
	Draft  bool
	URL    string `json:"html_url"`
	Base   struct {
		SHA  string
		Repo struct {
			FullName string `json:"full_name"`
		}
	}
	Head struct{ SHA string }
}

func (pr githubPR) valid(repository string, number int) bool {
	return pr.Number == number && pr.State == "open" && !pr.Draft && pr.Base.Repo.FullName == repository
}
