package platform

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

type (
	githubRepository struct {
		Name string `json:"full_name"`
		URL  string `json:"clone_url"`
	}
	githubSide struct {
		SHA  string           `json:"sha"`
		Ref  string           `json:"ref"`
		Repo githubRepository `json:"repo"`
	}
	githubReview struct {
		Number int        `json:"number"`
		Title  string     `json:"title"`
		Body   string     `json:"body"`
		Head   githubSide `json:"head"`
		Base   githubSide `json:"base"`
	}
)

func (s *Source) api(target Target) string {
	if base := s.config.APIBaseURLs[target.Host]; base != "" {
		return strings.TrimRight(base, "/")
	}
	if target.Kind == "github" {
		return "https://api.github.com"
	}
	return "https://" + target.Host + "/api/v4"
}

func (s *Source) metadata(ctx context.Context, target Target, full bool) (ReviewContext, error) {
	var metadata ReviewContext
	var err error
	if target.Kind == "github" {
		metadata, err = s.github(ctx, target, full)
	} else {
		metadata, err = s.gitlab(ctx, target, full)
	}
	if err != nil {
		return metadata, err
	}
	if !sha(metadata.Head) || !sha(metadata.TargetSHA) {
		return metadata, failure("source_unavailable", "platform returned invalid commit identity", nil)
	}
	if !repositoryURL(metadata.HeadURL, target.Host, metadata.HeadRepository) || !repositoryURL(metadata.TargetURL, target.Host, target.Repository) {
		return metadata, failure("source_unavailable", "platform returned untrusted repository identity", nil)
	}
	return metadata, nil
}

func sha(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func trustedRemote(raw, host string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == host && u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.HasSuffix(u.Path, ".git")
}

func (s *Source) github(ctx context.Context, target Target, full bool) (ReviewContext, error) {
	var review githubReview
	root := s.api(target) + "/repos/" + target.Repository
	endpoint := root + "/pulls/" + target.Number
	_, err := s.get(ctx, endpoint, target.Host, target.Kind, &review)
	if err != nil {
		return ReviewContext{}, err
	}
	if strconv.Itoa(review.Number) != target.Number || review.Base.Repo.Name != target.Repository {
		return ReviewContext{}, failure("source_unavailable", "platform repository identity mismatch", nil)
	}
	result := ReviewContext{SchemaVersion: 1, Target: target, Title: review.Title, Body: review.Body, Head: review.Head.SHA, TargetSHA: review.Base.SHA, HeadRepository: review.Head.Repo.Name, HeadRef: review.Head.Ref, TargetRef: review.Base.Ref, HeadURL: review.Head.Repo.URL, TargetURL: review.Base.Repo.URL, Items: map[string][]json.RawMessage{}}
	if !full {
		return result, nil
	}
	paths := map[string]string{"files": endpoint + "/files", "commits": endpoint + "/commits", "reviews": endpoint + "/reviews", "review_comments": endpoint + "/comments", "comments": root + "/issues/" + target.Number + "/comments", "statuses": root + "/commits/" + result.Head + "/statuses"}
	if err = s.collections(ctx, target, paths, result.Items); err != nil {
		return result, err
	}
	result.Items["checks"], err = s.paged(ctx, root+"/commits/"+result.Head+"/check-runs?per_page=100", target.Host, target.Kind, "check_runs")
	return result, err
}

func (s *Source) collections(ctx context.Context, target Target, paths map[string]string, out map[string][]json.RawMessage) error {
	for name, path := range paths {
		items, err := s.pages(ctx, path+"?per_page=100", target.Host, target.Kind)
		if err != nil {
			return err
		}
		out[name] = items
	}
	return nil
}

type gitlabReview struct {
	IID       int    `json:"iid"`
	ProjectID int    `json:"project_id"`
	SourceID  int    `json:"source_project_id"`
	Title     string `json:"title"`
	Body      string `json:"description"`
	SHA       string `json:"sha"`
	Source    string `json:"source_branch"`
	Target    string `json:"target_branch"`
}
type gitlabProject struct {
	ID   int    `json:"id"`
	Path string `json:"path_with_namespace"`
	URL  string `json:"http_url_to_repo"`
}

func (s *Source) gitlab(ctx context.Context, target Target, full bool) (ReviewContext, error) {
	projectURL := s.api(target) + "/projects/" + url.PathEscape(target.Repository)
	var project gitlabProject
	if _, err := s.get(ctx, projectURL, target.Host, target.Kind, &project); err != nil {
		return ReviewContext{}, err
	}
	if project.Path != target.Repository || project.ID <= 0 {
		return ReviewContext{}, failure("source_unavailable", "platform repository identity mismatch", nil)
	}
	endpoint := projectURL + "/merge_requests/" + target.Number
	var review gitlabReview
	if _, err := s.get(ctx, endpoint, target.Host, target.Kind, &review); err != nil {
		return ReviewContext{}, err
	}
	if review.ProjectID != project.ID || strconv.Itoa(review.IID) != target.Number {
		return ReviewContext{}, failure("source_unavailable", "merge request identity mismatch", nil)
	}
	var branch struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if _, err := s.get(ctx, projectURL+"/repository/branches/"+url.PathEscape(review.Target), target.Host, target.Kind, &branch); err != nil {
		return ReviewContext{}, err
	}
	source, err := s.gitlabSource(ctx, target, review.SourceID, project)
	if err != nil {
		return ReviewContext{}, err
	}
	result := ReviewContext{SchemaVersion: 1, Target: target, Title: review.Title, Body: review.Body, Head: review.SHA, TargetSHA: branch.Commit.ID, HeadRepository: source.Path, HeadRef: review.Source, TargetRef: review.Target, HeadURL: source.URL, TargetURL: project.URL, Items: map[string][]json.RawMessage{}}
	if full {
		err := s.collections(ctx, target, map[string]string{"files": endpoint + "/diffs", "commits": endpoint + "/commits", "comments": endpoint + "/notes", "discussions": endpoint + "/discussions", "pipelines": endpoint + "/pipelines"}, result.Items)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Source) gitlabSource(ctx context.Context, target Target, id int, project gitlabProject) (gitlabProject, error) {
	source := project
	if id != project.ID {
		if _, err := s.get(ctx, s.api(target)+"/projects/"+strconv.Itoa(id), target.Host, target.Kind, &source); err != nil {
			return source, err
		}
	}
	if source.ID != id {
		return source, failure("source_unavailable", "source project identity mismatch", nil)
	}
	return source, nil
}

func repositoryURL(raw, host, repository string) bool {
	if !validRepository(repository) || !trustedRemote(raw, host) {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Path == "/"+repository+".git"
}
