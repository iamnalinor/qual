package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/iamnalinor/qual/internal/budget"
	"gopkg.in/yaml.v3"
)

const reviewerAuthorizationFile = ".config-reviewer-authorization.yaml"

type reviewerLimit struct {
	Hard     string `yaml:"hard,omitempty"`
	Origin   string `yaml:"origin"`
	Selected bool   `yaml:"selected"`
}

type reviewerAuthorization struct {
	SchemaVersion int                      `yaml:"schema_version"`
	Reviewers     map[string]reviewerLimit `yaml:"reviewers"`
}

func userReviewerAuthorization(home string) (map[string]string, error) {
	data, err := readOptional(filepath.Join(home, "settings.yaml"))
	if err != nil {
		return nil, err
	}
	values, err := existingSettings(data)
	if err != nil {
		return nil, err
	}
	return authorizationRecords(values)
}

func authorizationRecords(settings map[string]any) (map[string]string, error) {
	list, err := reviewerList(settings["reviewers"])
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, item := range list {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, invalid("invalid reviewer authorization")
		}
		id := fmt.Sprint(record["id"])
		if !safeName(id) {
			return nil, invalid("invalid reviewer authorization ID")
		}
		hard := recordHard(record)
		if err = validateAuthorizationLimit(hard); err != nil {
			return nil, err
		}
		result[id] = hard
	}
	return result, nil
}

func recordHard(record map[string]any) string {
	if value, ok := record["hard"]; ok {
		return fmt.Sprint(value)
	}
	return ""
}

func readReviewerAuthorization(root string) (reviewerAuthorization, error) {
	result := reviewerAuthorization{SchemaVersion: 1, Reviewers: map[string]reviewerLimit{}}
	data, err := readOptional(filepath.Join(root, reviewerAuthorizationFile))
	if err != nil {
		return result, err
	}
	if len(data) == 0 {
		return result, nil
	}
	result.SchemaVersion = 0
	if err = decode(data, &result); err != nil {
		return result, err
	}
	if result.SchemaVersion != 1 {
		return result, invalid("unsupported reviewer authorization version")
	}
	if result.Reviewers == nil {
		result.Reviewers = map[string]reviewerLimit{}
	}
	for id, limit := range result.Reviewers {
		if err = validateReviewerLimit(id, limit); err != nil {
			return result, err
		}
	}
	return result, nil
}

func validateReviewerLimit(id string, limit reviewerLimit) error {
	if !safeName(id) {
		return invalid("invalid reviewer authorization ID")
	}
	if limit.Origin != "user" && limit.Origin != "personal" {
		return invalid("invalid reviewer authorization origin")
	}
	return validateAuthorizationLimit(limit.Hard)
}

func validateAuthorizationLimit(hard string) error {
	if hard == "" {
		return nil
	}
	if _, err := budget.Parse(hard); err != nil {
		return invalid("invalid reviewer authorization limit")
	}
	return nil
}

func saveReviewerAuthorization(stage string, records map[string]reviewerLimit) error {
	data, err := yaml.Marshal(reviewerAuthorization{SchemaVersion: 1, Reviewers: records})
	if err != nil {
		return fmt.Errorf("encode reviewer authorization: %w", err)
	}
	if len(data) > maxFileSize {
		return invalid("reviewer authorization exceeds size limit")
	}
	if err = os.WriteFile(filepath.Join(stage, reviewerAuthorizationFile), data, 0o600); err != nil {
		return fmt.Errorf("stage reviewer authorization: %w", err)
	}
	return nil
}

func authorizationValues(records map[string]reviewerLimit) map[string]any {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	list := make([]any, 0, len(ids))
	for _, id := range ids {
		record := map[string]any{"id": id}
		if records[id].Hard != "" {
			record["hard"] = records[id].Hard
		}
		list = append(list, record)
	}
	return map[string]any{"reviewers": list}
}
