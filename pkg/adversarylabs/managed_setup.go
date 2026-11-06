package adversarylabs

import "context"

type ManagedSetupRequest struct {
	Resumable       bool                     `json:"resumable"`
	PortableState   bool                     `json:"portableState,omitempty"`
	Request         string                   `json:"request"`
	CodeChallenge   string                   `json:"codeChallenge"`
	Callback        string                   `json:"callback"`
	Profile         string                   `json:"profile"`
	API             string                   `json:"api"`
	AllRepositories bool                     `json:"allRepositories"`
	Repositories    []ManagedSetupRepository `json:"repositories"`
	Agents          []string                 `json:"agents"`
}

type ManagedSetupRepository struct {
	MainCheckout   string `json:"mainCheckout"`
	Checkout       string `json:"checkout"`
	AlreadyGranted bool   `json:"alreadyGranted,omitempty"`
}

type ManagedSetupResponse struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expiresIn"`
}

type ManagedSetupResult struct {
	TokenResponse
	State         string `json:"state"`
	AccountID     string `json:"account_id"`
	PortableState *bool  `json:"portable_state"`
}

func (c Client) CreateManagedSetup(ctx context.Context, credential string, input ManagedSetupRequest) (ManagedSetupResponse, error) {
	var out ManagedSetupResponse
	err := c.postJSON(ctx, "/v1/setup/grants", input, credential, &out)
	return out, err
}

func (c Client) PollManagedSetup(ctx context.Context, request, verifier string) (ManagedSetupResult, error) {
	var out ManagedSetupResult
	err := c.postJSON(ctx, "/v1/setup/grants/poll", map[string]string{"request": request, "codeVerifier": verifier}, "", &out)
	return out, err
}
