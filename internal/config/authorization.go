package config

import (
	"fmt"

	"github.com/iamnalinor/qual/internal/budget"
)

// retainReviewerCaps preserves externally authorized constraints while replacing the list.
func retainReviewerCaps(prior, proposed map[string]any) (map[string]any, error) {
	incoming, changes := proposed["reviewers"]
	if !changes {
		return nil, nil
	}
	before, err := reviewerList(prior["reviewers"])
	if err != nil {
		return nil, err
	}
	caps, ids, err := reviewerCaps(before)
	if err != nil {
		return nil, err
	}
	if len(caps) == 0 {
		return nil, nil
	}
	after, err := reviewerList(incoming)
	if err != nil {
		return nil, err
	}
	retained := map[string]any{}
	for _, item := range after {
		reviewer, ok := item.(map[string]any)
		if !ok {
			return nil, invalid("invalid reviewer configuration")
		}
		id := fmt.Sprint(reviewer["id"])
		if !ids[id] {
			return nil, invalid("restricted reviewer replacement cannot introduce IDs outside the externally capped reviewer set")
		}
		if hard, ok := caps[id]; ok {
			reviewer["hard"] = hard
			retained[id] = hard
		}
	}
	return retained, nil
}

func reviewerCaps(reviewers []any) (map[string]any, map[string]bool, error) {
	caps := map[string]any{}
	ids := map[string]bool{}
	for _, item := range reviewers {
		reviewer, ok := item.(map[string]any)
		if !ok {
			return nil, nil, invalid("invalid reviewer configuration")
		}
		id := fmt.Sprint(reviewer["id"])
		ids[id] = true
		hard, ok := reviewer["hard"]
		if !ok {
			continue
		}
		amount, err := budget.Parse(fmt.Sprint(hard))
		if err != nil {
			return nil, nil, invalid("invalid external hard budget")
		}
		if amount > 0 {
			caps[id] = hard
		}
	}
	return caps, ids, nil
}

func reviewerList(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, invalid("invalid reviewer list")
	}
	return list, nil
}
