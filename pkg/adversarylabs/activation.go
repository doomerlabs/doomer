package adversarylabs

import "context"

// ActivationEvent deliberately excludes source, prompts, Git identity and local paths.
type ActivationEvent struct {
	InstallationID string `json:"installationId"`
	Repository     string `json:"repository"`
	Agent          string `json:"agent"`
	Client         string `json:"client"`
	OS             string `json:"os"`
	Architecture   string `json:"architecture"`
	CLIVersion     string `json:"cliVersion"`
	SkillRevision  string `json:"skillRevision,omitempty"`
	ActivatedAt    string `json:"activatedAt"`
}

func (c Client) ReportActivation(ctx context.Context, token string, event ActivationEvent) error {
	return c.postJSON(ctx, "/v1/installations/activate", event, token, nil)
}
