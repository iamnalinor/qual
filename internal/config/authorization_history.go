package config

import (
	"fmt"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Authorization survives list replacement; inherited caps keep their original
// user origin so later trusted user updates do not become stale personal limits.
func proposalAuthorization(home, stage string, personal map[string]any) (map[string]reviewerLimit, error) {
	user, err := userReviewerAuthorization(home)
	if err != nil {
		return nil, err
	}
	history, err := readReviewerAuthorization(stage)
	if err != nil {
		return nil, err
	}
	inherited, err := refreshInheritedCaps(personal, history.Reviewers, user)
	if err != nil {
		return nil, err
	}
	current, err := authorizationRecords(personal)
	if err != nil {
		return nil, err
	}
	result := historicalAuthorization(user, history.Reviewers)
	for id, hard := range current {
		origin := "personal"
		if inherited[id] {
			origin = "user"
		}
		result[id] = reviewerLimit{Hard: hard, Origin: origin, Selected: true}
	}
	return result, nil
}

func historicalAuthorization(user map[string]string, history map[string]reviewerLimit) map[string]reviewerLimit {
	result := map[string]reviewerLimit{}
	for id, hard := range user {
		result[id] = reviewerLimit{Hard: hard, Origin: "user"}
	}
	for id, limit := range history {
		if limit.Origin == "user" {
			limit.Hard = user[id]
		}
		limit.Selected = false
		result[id] = limit
	}
	return result
}

// A changed or newly added personal hard field is an explicit trusted edit.
// Only an unchanged field previously written as inherited follows global updates.
func refreshInheritedCaps(personal map[string]any, history map[string]reviewerLimit, user map[string]string) (map[string]bool, error) {
	list, err := reviewerList(personal["reviewers"])
	if err != nil {
		return nil, err
	}
	inherited := map[string]bool{}
	for _, item := range list {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, invalid("invalid reviewer authorization")
		}
		id := fmt.Sprint(record["id"])
		previous, ok := history[id]
		if !ok || previous.Origin != "user" || !previous.Selected || recordHard(record) != previous.Hard {
			continue
		}
		inherited[id] = true
		if user[id] == "" {
			delete(record, "hard")
		} else {
			record["hard"] = user[id]
		}
	}
	return inherited, nil
}

func finalizeReviewerAuthorization(stage string, authority map[string]reviewerLimit, personal map[string]any) error {
	current, err := authorizationRecords(personal)
	if err != nil {
		return err
	}
	for id, record := range authority {
		record.Selected = false
		authority[id] = record
	}
	for id, hard := range current {
		record, ok := authority[id]
		if !ok {
			record.Origin = "user"
		}
		record.Hard = hard
		record.Selected = true
		authority[id] = record
	}
	return saveReviewerAuthorization(stage, authority)
}

func refreshPersonalLayer(home, root string, data []byte) ([]byte, map[string]Origin, error) {
	if len(data) == 0 {
		return data, nil, nil
	}
	personal, err := existingSettings(data)
	if err != nil {
		return nil, nil, err
	}
	history, err := readReviewerAuthorization(root)
	if err != nil {
		return nil, nil, err
	}
	user, err := userReviewerAuthorization(home)
	if err != nil {
		return nil, nil, err
	}
	inherited, err := refreshInheritedCaps(personal, history.Reviewers, user)
	if err != nil {
		return nil, nil, err
	}
	if len(inherited) == 0 {
		return data, nil, nil
	}
	result, err := yaml.Marshal(personal)
	if err != nil {
		return nil, nil, fmt.Errorf("encode inherited limits: %w", err)
	}
	origins := map[string]Origin{}
	for id := range inherited {
		origins[id] = Origin{Source: "user", Path: filepath.Join(home, "settings.yaml")}
	}
	return result, origins, nil
}
