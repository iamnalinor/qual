package citemplate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

type gitlabEvent struct {
	Kind       string `json:"object_kind"`
	Project    struct{ ID int }
	User       struct{ ID int }
	Attributes struct {
		IID          int
		Action       string
		Note         string
		NoteableType string `json:"noteable_type"`
	} `json:"object_attributes"`
	MR      struct{ IID int } `json:"merge_request"`
	Changes struct {
		Labels *struct {
			Previous []struct{ Title string }
			Current  []struct{ Title string }
		}
	}
}

func gitlabTrigger(in Input) (int, string, error) {
	if in.Event == "web" {
		return in.ManualNumber, in.Actor, nil
	}
	if in.Event != "trigger" {
		return 0, "", fmt.Errorf("ci_event: GitLab requires trusted trigger or manual web pipeline")
	}
	var event gitlabEvent
	if err := json.Unmarshal(in.Payload, &event); err != nil {
		return 0, "", fmt.Errorf("ci_event: decode GitLab webhook: %w", err)
	}
	if strconv.Itoa(event.Project.ID) != in.Repository {
		return 0, "", fmt.Errorf("ci_event: project mismatch")
	}
	switch event.Kind {
	case "merge_request":
		if !gitlabAction(event) {
			return 0, "", fmt.Errorf("ci_skipped: unsupported merge request action")
		}
		return event.Attributes.IID, strconv.Itoa(event.User.ID), nil
	case "note":
		if event.Attributes.Note != "/qual review" || event.Attributes.NoteableType != "MergeRequest" {
			return 0, "", fmt.Errorf("ci_skipped: unsupported note")
		}
		return event.MR.IID, strconv.Itoa(event.User.ID), nil
	default:
		return 0, "", fmt.Errorf("ci_event: unsupported GitLab webhook")
	}
}

func addedReviewLabel(event gitlabEvent) bool {
	for _, label := range event.Changes.Labels.Previous {
		if label.Title == "qual-review" {
			return false
		}
	}
	for _, label := range event.Changes.Labels.Current {
		if label.Title == "qual-review" {
			return true
		}
	}
	return false
}

func authorizeGitLab(ctx context.Context, in Input, client HTTPDoer) (Decision, error) {
	project, err := strconv.Atoi(in.Repository)
	if err != nil || project <= 0 {
		return Decision{}, fmt.Errorf("ci_invalid: GitLab requires numeric project ID")
	}
	number, actor, err := gitlabTrigger(in)
	if err != nil {
		return Decision{}, err
	}
	user, err := strconv.Atoi(actor)
	if err != nil || user <= 0 || number <= 0 {
		return Decision{}, fmt.Errorf("ci_event: invalid merge request or actor")
	}
	var mr gitlabMR
	path := "/projects/" + in.Repository
	if err := apiGet(ctx, in, client, fmt.Sprintf("%s/merge_requests/%d", path, number), &mr); err != nil {
		return Decision{}, err
	}
	if !mr.valid(project, number) {
		return Decision{}, fmt.Errorf("ci_skipped: merge request is draft, closed, or mismatched")
	}
	if err := gitlabPermission(ctx, in, client, actor, user); err != nil {
		return Decision{}, err
	}
	var branch struct{ Commit struct{ ID string } }
	if err := apiGet(ctx, in, client, path+"/repository/branches/"+url.PathEscape(mr.TargetBranch), &branch); err != nil {
		return Decision{}, err
	}
	return checkedDecision(Decision{Provider: in.Provider, Repository: in.Repository, Number: number, URL: mr.URL, Actor: actor, TargetSHA: branch.Commit.ID, HeadSHA: mr.SHA})
}

func gitlabAction(event gitlabEvent) bool {
	switch event.Attributes.Action {
	case "open", "reopen":
		return true
	case "update":
		return event.Changes.Labels == nil || addedReviewLabel(event)
	default:
		return false
	}
}

func gitlabPermission(ctx context.Context, in Input, client HTTPDoer, actor string, user int) error {
	var member struct {
		ID          int
		AccessLevel int     `json:"access_level"`
		ExpiresAt   *string `json:"expires_at"`
	}
	if err := apiGet(ctx, in, client, "/projects/"+in.Repository+"/members/all/"+actor, &member); err != nil {
		return err
	}
	if member.ID != user || member.AccessLevel < 30 || !membershipActive(member.ExpiresAt) {
		return fmt.Errorf("ci_denied: initiator requires active developer permission")
	}

	return nil
}

type gitlabMR struct {
	IID            int
	ProjectID      int `json:"project_id"`
	State          string
	Draft          bool
	WorkInProgress bool   `json:"work_in_progress"`
	URL            string `json:"web_url"`
	SHA            string
	TargetBranch   string `json:"target_branch"`
}

func (mr gitlabMR) valid(project, number int) bool {
	return mr.IID == number && mr.ProjectID == project && mr.State == "opened" && !mr.Draft && !mr.WorkInProgress && mr.TargetBranch != ""
}

func membershipActive(expiry *string) bool {
	if expiry == nil {
		return true
	}
	date, err := time.Parse(time.DateOnly, *expiry)
	if err != nil {
		return false
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	return !date.Before(today)
}
