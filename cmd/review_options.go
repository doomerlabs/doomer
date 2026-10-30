package cmd

import (
	"fmt"
	"strings"
)

type reviewWaitPolicy struct {
	lookCloser bool
	fullReview bool
}

func resolveReviewWait(value string, explicit, lookCloser, fullReview bool) (reviewWaitPolicy, error) {
	if !lookCloser && !fullReview {
		return reviewWaitPolicy{}, fmt.Errorf("enable at least one of --look-closer or --deep-review")
	}
	if !explicit {
		return reviewWaitPolicy{lookCloser: lookCloser}, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "false":
		return reviewWaitPolicy{}, nil
	case "all", "true":
		return reviewWaitPolicy{lookCloser: lookCloser, fullReview: fullReview}, nil
	case "look-closer":
		if lookCloser {
			return reviewWaitPolicy{lookCloser: true}, nil
		}
		return reviewWaitPolicy{}, fmt.Errorf("--wait=look-closer requires --look-closer")
	case "deep":
		if fullReview {
			return reviewWaitPolicy{fullReview: true}, nil
		}
		return reviewWaitPolicy{}, fmt.Errorf("--wait=deep requires --deep-review")
	default:
		return reviewWaitPolicy{}, fmt.Errorf("--wait must be none, look-closer, deep, or all")
	}
}
