package adversarylabs

import (
	"encoding/base64"
	"net/url"
	"regexp"
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

var workspaceLinkPath = regexp.MustCompile(`^/look-closer/[a-z0-9][a-z0-9_.~-]*/[a-z0-9][a-z0-9_.~-]*/[a-f0-9]{64}$`)

func WorkspaceLink(api string, review WorkspaceReview) string {
	base := strings.TrimSuffix(strings.TrimRight(api, "/"), "/api")
	supplied, err := url.Parse(review.URL)
	if err != nil {
		return ""
	}
	origin, err := url.Parse(base)
	if err != nil {
		return ""
	}
	if supplied.Scheme != origin.Scheme || supplied.Host != origin.Host || supplied.User != nil || !workspaceLinkPath.MatchString(supplied.Path) {
		return ""
	}
	return supplied.String()
}
