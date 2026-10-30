package adversarylabs

import (
	"encoding/base64"
	"net/url"
	"strings"
)

const AnonymousTokenPrefix = "doomer_anon_"

func IsAnonymousToken(token string) bool {
	if !strings.HasPrefix(token, AnonymousTokenPrefix) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, AnonymousTokenPrefix))
	return err == nil && len(decoded) == 32
}
func WorkspaceLink(api string, review WorkspaceReview) string {
	base := strings.TrimSuffix(strings.TrimRight(api, "/"), "/api")
	fallback := base + "/worktree/reviews/" + review.ID
	supplied, err := url.Parse(review.URL)
	if err != nil {
		return fallback
	}
	origin, err := url.Parse(base)
	if err != nil {
		return fallback
	}
	if supplied.Scheme != origin.Scheme || supplied.Host != origin.Host || supplied.User != nil {
		return fallback
	}
	if supplied.Path != "/look-closer/"+review.ID && supplied.Path != "/worktree/reviews/"+review.ID {
		return fallback
	}
	return supplied.String()
}
