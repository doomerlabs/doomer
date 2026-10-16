package adversarylabs

import (
	"context"
	"fmt"
	"github.com/doomerlabs/doomer/pkg/namespacesig"
	"net/http"
)

type WhoamiResponse struct {
	ID                string       `json:"id,omitempty"`
	Name              string       `json:"name,omitempty"`
	Email             string       `json:"email,omitempty"`
	EmailAddress      string       `json:"email_address,omitempty"`
	RegistryNamespace string       `json:"registry_namespace,omitempty"`
	Namespace         string       `json:"namespace,omitempty"`
	Team              Team         `json:"team,omitempty"`
	Teams             []Team       `json:"teams,omitempty"`
	Organization      Team         `json:"organization,omitempty"`
	Subscription      Subscription `json:"subscription,omitempty"`
}

type Team struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Slug string `json:"slug,omitempty"`
}

type Subscription struct {
	Name   string `json:"name,omitempty"`
	Plan   string `json:"plan,omitempty"`
	Status string `json:"status,omitempty"`
}

func (c Client) Whoami(ctx context.Context, token string) (WhoamiResponse, error) {
	if _, err := validateBaseURL(c.BaseURL); err != nil {
		return WhoamiResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/auth/whoami", nil)
	if err != nil {
		return WhoamiResponse{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return WhoamiResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return WhoamiResponse{}, fmt.Errorf("not logged in; run doomer login")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WhoamiResponse{}, fmt.Errorf("whoami failed: %s", resp.Status)
	}
	var out WhoamiResponse
	if err := decodeLimited(resp.Body, &out); err != nil {
		return WhoamiResponse{}, err
	}
	return out, nil
}

func (c Client) NamespaceTrustRoot(ctx context.Context, token string) (namespacesig.Root, error) {
	if _, err := validateBaseURL(c.BaseURL); err != nil {
		return namespacesig.Root{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/registry/sign/root", nil)
	if err != nil {
		return namespacesig.Root{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return namespacesig.Root{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return namespacesig.Root{}, fmt.Errorf("namespace trust root request failed: %s", resp.Status)
	}
	var out namespacesig.Root
	if err := decodeLimited(resp.Body, &out); err != nil {
		return namespacesig.Root{}, err
	}
	return out, nil
}
