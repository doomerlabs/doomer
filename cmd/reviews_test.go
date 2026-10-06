package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/snapshot"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestRunResumesCapturedSubmissionAfterSourceRemoval(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, b)
		}
	}
	runGit("init", "--template=")
	os.WriteFile(filepath.Join(repo, "code.go"), []byte("original\n"), 0644)
	runGit("add", ".")
	runGit("commit", "-m", "initial")
	os.WriteFile(filepath.Join(repo, "code.go"), []byte("captured\n"), 0644)
	dataDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("DOOMER_DATA_DIR", dataDir)
	t.Setenv("DOOMER_CONFIG_DIR", configDir)
	var digest string
	var captured []byte
	var creates, uploads, finalizes int
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing auth")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/reviews/submissions":
			creates++
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			digest = body["digest"]
			if body["project"] != "project" {
				t.Error("missing project")
			}
			json.NewEncoder(w).Encode(adversarylabs.ReviewStatus{ID: "review-1", Status: "uploading", Digest: digest})
		case "PUT /api/v1/reviews/submissions/review-1/snapshot":
			uploads++
			var b json.RawMessage
			json.NewDecoder(r.Body).Decode(&b)
			if fail {
				fail = false
				w.WriteHeader(503)
				return
			}
			captured = b
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "POST /api/v1/reviews/submissions/review-1/finalize":
			finalizes++
			json.NewEncoder(w).Encode(adversarylabs.ReviewStatus{ID: "review-1", Status: "queued", Digest: digest})
		case "GET /api/v1/reviews/submissions/review-1":
			json.NewEncoder(w).Encode(adversarylabs.ReviewStatus{ID: "review-1", Status: "completed", Digest: digest, Result: json.RawMessage(`{"summary":"Done"}`)})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	store := adversarylabs.ConfigStore{Path: filepath.Join(configDir, "config.json")}
	if e := store.SetAuth(adversarylabs.AuthKey(server.URL+"/api", "default"), adversarylabs.Auth{Token: "secret"}); e != nil {
		t.Fatal(e)
	}
	execute := func(args ...string) error {
		root := NewRootCommand()
		root.SetArgs(append([]string{"--api-url", server.URL + "/api"}, args...))
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		return root.Execute()
	}
	if e := execute("run", "--project", "project", "--path", repo, "--async"); e == nil {
		t.Fatal("expected upload interruption")
	}
	files, e := os.ReadDir(filepath.Join(dataDir, "submissions"))
	if e != nil || len(files) != 1 {
		t.Fatalf("no durable submission: %v", e)
	}
	key := strings.TrimSuffix(files[0].Name(), ".json")
	os.RemoveAll(repo)
	if e = execute("run", "--resume", key, "--async"); e != nil {
		t.Fatal(e)
	}
	if creates != 1 || uploads != 2 || finalizes != 1 {
		t.Fatalf("unexpected lifecycle %d/%d/%d", creates, uploads, finalizes)
	}
	if snapshot.Digest(captured) != digest {
		t.Fatal("resumed different snapshot")
	}
	m, e := snapshot.Decode(captured)
	if e != nil {
		t.Fatal(e)
	}
	if string(m.Blobs[m.Target[0].Hash]) != "captured\n" {
		t.Fatal("did not freeze source")
	}
	files, _ = os.ReadDir(filepath.Join(dataDir, "submissions"))
	if len(files) != 0 {
		t.Fatal("accepted source retained locally")
	}
	if e = execute("reviews", "show", "review-1"); e != nil {
		t.Fatal(e)
	}
}
