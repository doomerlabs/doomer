package githubreview

import "fmt"

// CommentStyle controls presentation only. Findings, confidence, and placement
// are determined before the rewrite runs.
type CommentStyle struct {
	Tone        string `json:"tone"`
	Conciseness string `json:"conciseness"`
	Politeness  string `json:"politeness"`
	Formality   string `json:"formality"`
}

func (s CommentStyle) Normalize() (CommentStyle, error) {
	if s.Tone == "" {
		s.Tone = "direct"
	}
	if s.Conciseness == "" {
		s.Conciseness = "terse"
	}
	if s.Politeness == "" {
		s.Politeness = "medium"
	}
	if s.Formality == "" {
		s.Formality = "high"
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
	switch s.Politeness {
	case "very-low", "low", "medium", "high":
	default:
		return s, fmt.Errorf("comment politeness must be very-low, low, medium, or high (got %q)", s.Politeness)
	}
	switch s.Formality {
	case "low", "medium", "high":
	default:
		return s, fmt.Errorf("comment formality must be low, medium, or high (got %q)", s.Formality)
	}
	return s, nil
}
