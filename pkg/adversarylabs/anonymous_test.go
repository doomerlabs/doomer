package adversarylabs

import (
	"strings"
	"testing"
)

func TestAnonymousTokensAndUnlistedLinks(t *testing.T) {
	if !IsAnonymousToken(AnonymousTokenPrefix+strings.Repeat("A", 43)) || IsAnonymousToken("access-token") || IsAnonymousToken(AnonymousTokenPrefix+"bad") {
		t.Fatal("invalid token recognition")
	}
	r := WorkspaceReview{ID: "review", URL: "https://example.test/look-closer/org/repo/" + strings.Repeat("a", 64) + "#private"}
	if got := WorkspaceLink("https://example.test/api", r); got != r.URL {
		t.Fatal(got)
	}
	r.URL = "https://evil.test/look-closer/review#private"
	if got := WorkspaceLink("https://example.test/api", r); got != "" {
		t.Fatal(got)
	}
	for _, path := range []string{"/look-closer/review", "/worktree/reviews/review", "/look-closer/org/repo/short"} {
		r.URL = "https://example.test" + path
		if got := WorkspaceLink("https://example.test/api", r); got != "" {
			t.Fatalf("accepted old or invalid link: %s", got)
		}
	}
}
