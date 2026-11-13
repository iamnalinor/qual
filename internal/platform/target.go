// Package platform captures immutable GitHub and GitLab review sources.
package platform

import (
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iamnalinor/qual/internal/domain"
)

// Target identifies a hosted pull or merge request.
type Target struct {
	Kind       string `json:"kind"`
	Host       string `json:"host"`
	Repository string `json:"repository"`
	Number     string `json:"number"`
	URL        string `json:"url"`
}

var repositoryPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func failure(code, message string, err error) error {
	return &domain.Error{Code: domain.ErrorCode(code), Message: message, Cause: err}
}

// ParseTarget accepts HTTPS GitHub and explicitly trusted GitLab hosts.
func ParseTarget(raw string, gitlabHosts []string) (Target, error) {
	var result Target
	u, err := url.Parse(raw)
	if err != nil || !validReviewURL(u) {
		return result, failure("invalid_config", "invalid review URL", nil)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	kind, repository, number, err := targetLocation(u.Host, parts, gitlabHosts)
	if err != nil {
		return result, err
	}
	id, err := strconv.ParseUint(number, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != number {
		return result, failure("invalid_config", "invalid review number", nil)
	}
	if !validRepository(repository) {
		return result, failure("invalid_config", "invalid repository", nil)
	}
	return Target{Kind: kind, Host: u.Host, Repository: repository, Number: number, URL: raw}, nil
}

func validRepository(repository string) bool {
	for _, part := range strings.Split(repository, "/") {
		if part == "" || part == "." || part == ".." || !repositoryPart.MatchString(part) {
			return false
		}
	}
	return true
}

func validReviewURL(u *url.URL) bool {
	return u.Scheme == "https" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Host != ""
}

func targetLocation(host string, parts, gitlabHosts []string) (string, string, string, error) {
	if host == "github.com" && len(parts) == 4 && parts[2] == "pull" {
		return "github", strings.Join(parts[:2], "/"), parts[3], nil
	}
	n := len(parts)
	trusted := host == "gitlab.com" || slices.Contains(gitlabHosts, host)
	if !trusted || n < 5 {
		return "", "", "", failure("invalid_config", "unsupported review URL or host", nil)
	}
	if parts[n-3] != "-" || parts[n-2] != "merge_requests" {
		return "", "", "", failure("invalid_config", "unsupported GitLab review URL", nil)
	}
	return "gitlab", strings.Join(parts[:n-3], "/"), parts[n-1], nil
}
