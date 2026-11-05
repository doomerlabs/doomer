package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"strings"
	"testing"
	"time"
)

func TestSetupDesktopModes(t *testing.T) {
	for _, tc := range []struct {
		os   string
		env  map[string]string
		want bool
	}{
		{"darwin", nil, true}, {"darwin", map[string]string{"SSH_CONNECTION": "remote"}, false},
		{"linux", nil, false}, {"linux", map[string]string{"DISPLAY": ":0"}, true},
		{"linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
		{"linux", map[string]string{"DISPLAY": ":0", "SSH_TTY": "/dev/pts/0"}, false},
	} {
		if got := setupDesktop(tc.os, func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}

func TestSetupPrivacyURLFollowsDestination(t *testing.T) {
	for _, api := range []string{"http://localhost:3000/api", "https://doomer.ai/api"} {
		s := setupConsent{API: api}
		if got := s.PrivacyURL(); got != strings.TrimSuffix(api, "/api")+"/privacy" {
			t.Fatal(got)
		}
	}
}

func TestBrowserSetupRejectsInvalidDestination(t *testing.T) {
	for _, api := range []string{"", "file:///tmp/service", "https://user:password@example.test/api"} {
		_, err := browserSetupConsent(context.Background(), setupConsent{API: api}, io.Discard, func(context.Context, string) error { t.Fatal("opened browser for invalid destination"); return nil })
		if err == nil {
			t.Fatalf("accepted %q", api)
		}
	}
}

func TestSetupApprovalRequiresNonceAndIsSingleUse(t *testing.T) {
	for _, decision := range []string{"allow", "cancel"} {
		result := make(chan bool, 1)
		s := setupConsent{Nonce: strings.Repeat("a", 64)}
		handler := setupApprovalHandler(s, "127.0.0.1:1234", result)
		for _, path := range []string{"/complete?decision=" + decision, "/complete?decision=" + decision + "&nonce=wrong", "/other?decision=" + decision + "&nonce=" + s.Nonce} {
			req := httptest.NewRequest("GET", "http://127.0.0.1:1234"+path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusForbidden || len(result) != 0 {
				t.Fatal(response.Code)
			}
		}
		path := "http://127.0.0.1:1234/complete?decision=" + decision + "&nonce=" + s.Nonce
		wrongHost := httptest.NewRequest("GET", path, nil)
		wrongHost.Host = "evil.test"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, wrongHost)
		if response.Code != http.StatusForbidden {
			t.Fatal(response.Code)
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 || <-result != (decision == "allow") {
			t.Fatal(response.Code)
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != http.StatusGone || len(result) != 0 {
			t.Fatal(response.Code)
		}
	}
}

func TestBrowserSetupRoundTrip(t *testing.T) {
	var out bytes.Buffer
	s := setupConsent{API: "http://localhost:3000/api", Profile: "local", AllRepositories: true}
	approved, err := browserSetupConsent(context.Background(), s, &out, func(ctx context.Context, link string) error {
		u, _ := url.Parse(link)
		if u.Hostname() != "127.0.0.1" || u.Path != "/consent" || u.Query().Get("nonce") == "" {
			return fmt.Errorf("unexpected consent URL: %s", link)
		}
		page, err := http.Get(link)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(page.Body)
		page.Body.Close()
		if err != nil || !strings.Contains(string(body), "http://localhost:3000") || !strings.Contains(string(body), "task prompt") {
			return fmt.Errorf("missing disclosure: %s", body)
		}
		callback := *u
		callback.Path = "/complete"
		q := callback.Query()
		q.Set("decision", "allow")
		callback.RawQuery = q.Encode()
		response, err := http.Get(callback.String())
		if err != nil {
			return err
		}
		defer response.Body.Close()
		_, err = io.Copy(io.Discard, response.Body)
		return err
	})
	if err != nil || !approved {
		t.Fatalf("%v %v", approved, err)
	}
}

func TestTerminalSetupConsentCancelsBlockedRead(t *testing.T) {
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- terminalSetupConsent(ctx, input) }()
	cancel()
	select {
	case approved := <-done:
		if approved {
			t.Fatal("cancellation must not approve uploads")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock setup")
	}
}

func TestTerminalSetupConsentRequiresYes(t *testing.T) {
	for _, text := range []string{"no\n", "\n", "", "yes please\n"} {
		if terminalSetupConsent(context.Background(), strings.NewReader(text)) {
			t.Fatalf("accepted %q", text)
		}
	}
	if !terminalSetupConsent(context.Background(), strings.NewReader("yes\n")) {
		t.Fatal("rejected yes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if terminalSetupConsent(ctx, strings.NewReader("yes\n")) {
		t.Fatal("accepted after cancellation")
	}
}

func TestSetupPagesPreserveScopeAndApprovalMeaning(t *testing.T) {
	s := setupConsent{API: "https://doomer.ai/api", Profile: "default", Nonce: strings.Repeat("a", 64), Repositories: []setupRepository{{MainCheckout: "/repo/<script>alert(1)</script>", Checkout: "/linked/<script>alert(1)</script>"}}, Agents: []string{"codex"}}
	var out bytes.Buffer
	if err := setupPage.ExecuteTemplate(&out, "consent", s); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<script>alert(1)</script>") || !strings.Contains(out.String(), "&lt;script&gt;") {
		t.Fatal("repository name was not escaped")
	}
	for _, text := range []string{"all existing and future linked Git worktrees", "Allow for repo and worktrees", "outside the changed lines", "task prompt", "review responses", "/privacy", "name=\"nonce\"", "value=\"cancel\""} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing disclosure/control %q", text)
		}
	}
	s.AllRepositories = true
	out.Reset()
	if err := setupPage.ExecuteTemplate(&out, "consent", s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "future checkouts") || !strings.Contains(out.String(), "Allow for these repositories") {
		t.Fatal("all-repository scope hidden")
	}
	for _, approved := range []bool{true, false} {
		out.Reset()
		if err := setupPage.ExecuteTemplate(&out, "complete", struct {
			Consent  setupConsent
			Approved bool
		}{s, approved}); err != nil {
			t.Fatal(err)
		}
		if approved && (!strings.Contains(out.String(), "Permission received") || !strings.Contains(out.String(), "terminal will confirm whether setup finished")) {
			t.Fatal("approval incorrectly claims completed setup")
		}
		if !approved && (!strings.Contains(out.String(), "haven’t granted permission") || strings.Contains(out.String(), "Permission received")) {
			t.Fatal("decline incorrectly claims approval")
		}
	}
}

func TestSetupServiceURLHidesOnlyAPISuffix(t *testing.T) {
	for _, tc := range []struct{ api, want string }{
		{"https://doomer.ai/api", "https://doomer.ai"},
		{"http://localhost:3000/api", "http://localhost:3000"},
		{"https://example.com/service/api/", "https://example.com/service"},
	} {
		s := setupConsent{API: tc.api}
		if got := s.ServiceURL(); got != tc.want {
			t.Fatalf("%s: %s", tc.api, got)
		}
		var page bytes.Buffer
		if err := setupPage.ExecuteTemplate(&page, "consent", s); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(page.String(), `href="`+tc.want+`"`) || strings.Contains(page.String(), tc.api+`</`) {
			t.Fatal("page shows internal API endpoint")
		}
		if s.API != tc.api {
			t.Fatal("changed API destination")
		}
	}
}

func TestSetupConsentFromLinkedWorktree(t *testing.T) {
	main := t.TempDir()
	reviewGit(t, main, "init")
	reviewGit(t, main, "commit", "--allow-empty", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	reviewGit(t, main, "worktree", "add", "-b", "linked", linked)
	main, err := watching.Root(context.Background(), main)
	if err != nil {
		t.Fatal(err)
	}
	linked, err = watching.Root(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	mainRepos, err := setupRepositories(context.Background(), []string{main}, watching.Config{})
	if err != nil || len(mainRepos) != 1 || mainRepos[0].MainCheckout != main || mainRepos[0].Checkout != main {
		t.Fatalf("incorrect main checkout display: %+v, %v", mainRepos, err)
	}
	repos, err := setupRepositories(context.Background(), []string{linked}, watching.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].MainCheckout != main || repos[0].Checkout != linked {
		t.Fatalf("incorrect repository display: %+v", repos)
	}
	id, err := watching.RepositoryIdentity(context.Background(), main)
	if err != nil {
		t.Fatal(err)
	}
	granted, err := setupRepositories(context.Background(), []string{linked}, watching.Config{RepositoryIDs: []string{id}})
	if err != nil || !granted[0].AlreadyGranted {
		t.Fatalf("linked grant missing: %+v %v", granted, err)
	}
	if repos[0].AlreadyGranted {
		t.Fatal("unapproved checkout labeled granted")
	}
	consent := setupConsent{Repositories: repos}
	var page, terminal bytes.Buffer
	if err := setupPage.ExecuteTemplate(&page, "consent", consent); err != nil {
		t.Fatal(err)
	}
	consent.print(&terminal)
	for _, text := range []string{main, "all existing and future linked Git worktrees"} {
		if !strings.Contains(page.String(), text) || !strings.Contains(terminal.String(), text) {
			t.Fatalf("scope missing %q in browser or terminal", text)
		}
	}
}

func TestSetupConsentFromBareRepositoryWorktree(t *testing.T) {
	main := t.TempDir()
	reviewGit(t, main, "init")
	reviewGit(t, main, "commit", "--allow-empty", "-m", "initial")
	bare := filepath.Join(t.TempDir(), "repo.git")
	reviewGit(t, main, "clone", "--bare", main, bare)
	linked := filepath.Join(t.TempDir(), "linked")
	reviewGit(t, bare, "worktree", "add", "--detach", linked)
	linked, err := watching.Root(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := setupRepositories(context.Background(), []string{linked}, watching.Config{})
	if err != nil || len(repos) != 1 || repos[0].MainCheckout != "" || repos[0].Checkout != linked {
		t.Fatalf("incorrect bare repository display: %+v, %v", repos, err)
	}
}

func TestSetupConsentWithUnavailableMainCheckout(t *testing.T) {
	main := filepath.Join(t.TempDir(), "main")
	common := filepath.Join(t.TempDir(), "repository.git")
	if err := os.MkdirAll(main, 0700); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, main, "init", "--separate-git-dir", common)
	reviewGit(t, main, "commit", "--allow-empty", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	reviewGit(t, main, "worktree", "add", "--detach", linked)
	if err := os.RemoveAll(main); err != nil {
		t.Fatal(err)
	}
	repos, err := setupRepositories(context.Background(), []string{linked}, watching.Config{})
	if err != nil || len(repos) != 1 || repos[0].MainCheckout != "" || repos[0].Checkout != linked {
		t.Fatalf("unavailable display location blocked setup: %+v %v", repos, err)
	}
}

func TestSetupPageSeparatesPreviousGrants(t *testing.T) {
	s := setupConsent{Repositories: []setupRepository{
		{MainCheckout: "/previous", Checkout: "/previous-setup", AlreadyGranted: true},
		{MainCheckout: "/current", Checkout: "/current-setup"},
	}}
	var page bytes.Buffer
	if err := setupPage.ExecuteTemplate(&page, "consent", s); err != nil {
		t.Fatal(err)
	}
	html := page.String()
	if strings.Contains(html, "-setup") || strings.Contains(html, "Setup checkout") {
		t.Fatal("setup path displayed")
	}
	current, granted, previous := strings.Index(html, "/current"), strings.Index(html, "Already granted"), strings.Index(html, "/previous")
	if current < 0 || granted < current || previous < granted {
		t.Fatal("previous grants not separated below request")
	}
}
