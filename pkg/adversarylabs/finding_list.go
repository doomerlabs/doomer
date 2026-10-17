package adversarylabs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/doomerlabs/doomer/internal/version"
)

type FindingSummary struct {
	ID          string `json:"id"`
	Repository  string `json:"repository"`
	PullRequest int    `json:"pull_request"`
	Adversary   string `json:"adversary"`
}

func (c Client) ListFindings(ctx context.Context, token, project string, limit, offset int) ([]FindingSummary, error) {
	if strings.TrimSpace(project) == "" {
		return nil, errors.New("listing findings requires a project")
	}
	if limit < 1 || limit > 200 || offset < 0 {
		return nil, errors.New("invalid finding pagination")
	}
	if _, err := validateBaseURL(c.BaseURL); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/findings", nil)
	if err != nil {
		return nil, err
	}
	request.URL.RawQuery = url.Values{"project": {project}, "limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}.Encode()
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "doomer/"+version.Version)
	response, err := c.httpClient().Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("could not list findings")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case http.StatusUnauthorized:
			return nil, errors.New("login expired or invalid; run doomer login")
		case http.StatusForbidden, http.StatusNotFound:
			return nil, errors.New("findings unavailable; check project access and whether review details are enabled")
		default:
			return nil, errors.New("could not list findings; check your API endpoint and server version")
		}
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return nil, errors.New("finding endpoint did not return JSON; check your API endpoint and server version")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFindingDetailBytes+1))
	if err != nil || len(body) > maxFindingDetailBytes {
		return nil, errors.New("could not read finding list")
	}
	var result struct {
		Findings []FindingSummary `json:"findings"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, errors.New("finding endpoint did not return a valid list")
	}
	return result.Findings, nil
}
