package adversarylabs

import "context"

// ActivationEvent deliberately excludes source, prompts, Git identity and local paths.
type ActivationEvent struct {
	Stage          string `json:"stage"`
	ReviewID       string `json:"reviewId,omitempty"`
	AttemptID      string `json:"attemptId,omitempty"`
	InstallationID string `json:"installationId"`
	Repository     string `json:"repository"`
	Agent          string `json:"agent"`
	Client         string `json:"client"`
	OS             string `json:"os"`
	Architecture   string `json:"architecture"`
	CLIVersion     string `json:"cliVersion"`
	SkillRevision  string `json:"skillRevision,omitempty"`
	OccurredAt     string `json:"occurredAt"`
}

func (c Client) ReportActivation(ctx context.Context, token string, event ActivationEvent) error {
	return c.postJSON(ctx, "/v2/installations/activate", event, token, nil)
}
