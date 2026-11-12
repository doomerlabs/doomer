package adversarylabs

import (
	"encoding/base64"
	"net"
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

var accountWorkspaceLinkPath = regexp.MustCompile(`^/review/[^/]+/[^/]+/[^/]+/([1-9][0-9]*|(branch|commit|snapshot)/[^/]+)$`)

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
	if supplied.User != nil || (!workspaceLinkPath.MatchString(supplied.Path) && !accountWorkspaceLinkPath.MatchString(supplied.EscapedPath())) {
		return ""
	}
	for _, part := range strings.Split(supplied.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\\\r\n") {
			return ""
		}
	}
	sameOrigin := supplied.Scheme == origin.Scheme && supplied.Host == origin.Host
	host := origin.Hostname()
	address := net.ParseIP(host)
	localAPI := host == "localhost" || (address != nil && address.IsLoopback())
	// A local API can advertise its externally accessible HTTPS app origin.
	if !sameOrigin && !(localAPI && supplied.Scheme == "https" && supplied.Host != "") {
		return ""
	}
	return supplied.String()
}
