package adversarylabs

import (
	"strings"
	"testing"
)

func TestAnonymousTokensAndUnlistedLinks(t *testing.T) {
	if !IsAnonymousToken(AnonymousTokenPrefix+strings.Repeat("A", 43)) || IsAnonymousToken("access-token") || IsAnonymousToken(AnonymousTokenPrefix+"bad") {
		t.Fatal("invalid token recognition")
	}
	r := WorkspaceReview{ID: "review", URL: "https://example.test/look-closer/review#private"}
	if got := WorkspaceLink("https://example.test/api", r); got != r.URL {
		t.Fatal(got)
	}
	r.URL = "https://evil.test/look-closer/review#private"
	if got := WorkspaceLink("https://example.test/api", r); got != "https://example.test/worktree/reviews/review" {
		t.Fatal(got)
	}
}
