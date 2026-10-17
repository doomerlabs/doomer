package adversarylabs

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/doomerlabs/doomer/internal/version"
)

const maxFindingDetailBytes = 1 << 20

var findingIDPattern = regexp.MustCompile(`^[a-f0-9]{7,64}$`)

// FindingDetail retrieves stored evidence through authenticated team access.
// The server enforces the review_finding_details feature flag and permissions;
// short IDs require a project; secret-link tokens are never CLI credentials.
func (c Client) FindingDetail(ctx context.Context, token, id string, projects ...string) (string, error) {
	if !findingIDPattern.MatchString(id) {
		return "", errors.New("invalid finding ID: use the ID from the Doomer review comment")
	}
	project := ""
	if len(projects) > 0 {
		project = strings.TrimSpace(projects[0])
	}
	if len(id) < 64 && project == "" {
		return "", errors.New("short finding IDs require a project; use --project <slug>")
	}
	if _, err := validateBaseURL(c.BaseURL); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/findings/"+id+"/detail", nil)
	if err != nil {
		return "", err
	}
	if project != "" {
		request.URL.RawQuery = url.Values{"project": {project}}.Encode()
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "text/markdown")
	request.Header.Set("User-Agent", "doomer/"+version.Version)
	response, err := c.httpClient().Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("could not retrieve finding details")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return "", errors.New("login expired or invalid; run doomer login")
	case http.StatusForbidden:
		return "", errors.New("not authorized to read this finding; check your project access")
	case http.StatusNotFound:
		return "", errors.New("finding unavailable; check the finding ID, project access, and whether review details are enabled")
	default:
		return "", errors.New("finding details are temporarily unavailable")
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "text/markdown" {
		return "", errors.New("finding endpoint did not return markdown; check your API endpoint and server version")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFindingDetailBytes+1))
	if err != nil {
		return "", errors.New("could not read finding details")
	}
	if len(body) > maxFindingDetailBytes {
		return "", errors.New("finding details exceed the response limit")
	}
	return string(body), nil
}
