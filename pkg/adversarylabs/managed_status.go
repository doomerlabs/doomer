package adversarylabs

import "context"

// ManagedStatus contains only operational metadata. It cannot carry source,
// prompts, credentials, local paths, or signed artifact URLs.
type ManagedStatus struct {
	InstallationID string `json:"installation_id"`
	Repository     string `json:"repository"`
	Task           string `json:"task"`
	Agent          string `json:"agent"`
	Release        string `json:"release"`
	Protocol       int    `json:"protocol"`
	Policy         string `json:"policy"`
	State          string `json:"state"`
	Sequence       int64  `json:"sequence"`
	Paused         bool   `json:"paused"`
	HookActivated  bool   `json:"hook_activated"`
}

func (c Client) ReportManagedStatus(ctx context.Context, token string, status ManagedStatus) error {
	return c.postJSON(ctx, "/v3/installations/observe", status, token, nil)
}
