package githubreview

import "fmt"

// CommentStyle controls presentation only. Findings, confidence, and placement
// are determined before the rewrite runs.
type CommentStyle struct {
	Tone        string `json:"tone"`
	Conciseness string `json:"conciseness"`
}

func (s CommentStyle) Normalize() (CommentStyle, error) {
	if s.Tone == "" {
		s.Tone = "direct"
	}
	if s.Conciseness == "" {
		s.Conciseness = "terse"
	}
	switch s.Tone {
	case "direct", "neutral", "coaching":
	default:
		return s, fmt.Errorf("comment tone must be direct, neutral, or coaching (got %q)", s.Tone)
	}
	switch s.Conciseness {
	case "terse", "standard", "explanatory":
	default:
		return s, fmt.Errorf("comment conciseness must be terse, standard, or explanatory (got %q)", s.Conciseness)
	}
	return s, nil
}
