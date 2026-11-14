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

func TestWorkspaceLinksUsePublicOriginAndAccountRoutes(t *testing.T) {
	path := "/look-closer/org/repo/" + strings.Repeat("a", 64)
	for _, test := range []struct{ api, link string }{
		{"http://localhost:3000/api", "https://dev.example.exe.xyz" + path + "#review=private"},
		{"http://127.0.0.1:3000/api", "https://dev.example.exe.xyz/review/doomer/org/repo/branch/feature%2Fwork?revision=1&view=quick-review"},
		{"https://doomer.ai/api", "https://doomer.ai/review/team/org/repo/snapshot/review-id?view=quick-review"},
		{"https://doomer.ai/api", "https://doomer.ai/review/team/org/repo/123?view=quick-review"},
	} {
		if got := WorkspaceLink(test.api, WorkspaceReview{URL: test.link}); got != test.link {
			t.Errorf("lost review link: %q", got)
		}
	}
	for _, link := range []string{"javascript:alert(1)", "https://user:pass@example.test" + path, "https://example.test/account", "http://public.example" + path} {
		if got := WorkspaceLink("http://localhost:3000/api", WorkspaceReview{URL: link}); got != "" {
			t.Errorf("accepted invalid review URL %q", got)
		}
	}
}
