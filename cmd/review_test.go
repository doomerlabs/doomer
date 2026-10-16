package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func reviewGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Review Test", "GIT_AUTHOR_EMAIL=review@example.test", "GIT_COMMITTER_NAME=Review Test", "GIT_COMMITTER_EMAIL=review@example.test")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func TestReviewRequiresAuthentication(t *testing.T) {
	isolate(t)
	if _, err := execute(t, "", "review"); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("missing auth: %v", err)
	}
	if _, err := execute(t, "", "review", "--project", "my-team"); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("missing auth: %v", err)
	}
}

func TestReviewUploadsImmutableWorktreeUsingSelectedProfile(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	reviewGit(t, dir, "init", "-b", "main")
	code := filepath.Join(dir, "code.go")
	if err := os.WriteFile(code, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, dir, "add", ".")
	reviewGit(t, dir, "commit", "-m", "base")
	base := reviewGit(t, dir, "rev-parse", "HEAD")
	reviewGit(t, dir, "remote", "add", "origin", "git@github.com:owner/repository.git")
	if err := os.WriteFile(code, []byte("dirty worktree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var request adversarylabs.ReviewSubmissionRequest
	var tree sourcesnapshot.Manifest
	var methods []string
	var keys []string
	id := "11111111-1111-1111-1111-111111111111"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer work-token" {
			t.Error("wrong profile used for review")
			w.WriteHeader(401)
			return
		}
		methods = append(methods, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/reviews/submissions":
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			keys = append(keys, request.IdempotencyKey)
			if request.Project != "my-team" || request.Source.Branch != "main" || request.Source.HeadSHA != base || !request.Source.Dirty || request.Source.Repository != "github.com/owner/repository" {
				t.Errorf("wrong review source: %+v", request)
			}
			json.NewEncoder(w).Encode(adversarylabs.ReviewSubmission{ID: id, Digest: request.Digest, Status: "uploading"})
		case "/v1/reviews/submissions/" + id + "/snapshot":
			var raw json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if sourcesnapshot.Digest(raw) != request.Digest {
				t.Error("upload differs from submission digest")
			}
			var err error
			tree, err = sourcesnapshot.Decode(raw)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if string(tree.Blobs[tree.Target[0].Hash]) != "dirty worktree\n" {
				t.Error("did not upload captured working bytes")
			}
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "/v1/reviews/submissions/" + id + "/finalize":
			json.NewEncoder(w).Encode(adversarylabs.ReviewSubmission{
				ID: id, Digest: request.Digest, Status: "queued", Source: &request.Source,
				TreeHashVersion: sourcesnapshot.TreeHashVersion, BaseTreeHash: sourcesnapshot.TreeHash(tree.Base), TargetTreeHash: sourcesnapshot.TreeHash(tree.Target),
			})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	for profile, token := range map[string]string{"default": "wrong-token", "work": "work-token"} {
		if err := store.SetAuth(adversarylabs.AuthKey(api.URL, profile), adversarylabs.Auth{Token: token}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		out, err := execute(t, "", "--profile", "work", "--api-url", api.URL, "review", "--project", "my-team", "--path", dir, "--base", base, "--format", "json")
		if err != nil {
			t.Fatalf("review: %s %v", out, err)
		}
		// execute merges stderr progress with stdout; decode the final JSON line.
		lines := strings.Split(strings.TrimSpace(out), "\n")
		var result outputEnvelope[adversarylabs.ReviewSubmission]
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
			t.Fatal(err)
		}
		if result.Data.ID != id || result.Data.Status != "queued" || result.Data.TargetTreeHash == "" {
			t.Fatalf("bad review result: %+v", result)
		}
	}
	expected := []string{"POST /v1/reviews/submissions", "PUT /v1/reviews/submissions/" + id + "/snapshot", "POST /v1/reviews/submissions/" + id + "/finalize"}
	if len(methods) != 6 {
		t.Fatalf("wrong request count: %v", methods)
	}
	if !reflect.DeepEqual(methods[:3], expected) || !reflect.DeepEqual(methods[3:], expected) {
		t.Fatalf("wrong protocol order: %v", methods)
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal("repeating the capture did not retain idempotency")
	}
}
