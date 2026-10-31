package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestReviewReturnsLookCloserAndPreservesFullReviewChoice(t *testing.T) {
	for _, mode := range []struct {
		name             string
		look, full, wait bool
		mode             string
	}{
		{"default", true, true, false, ""},
		{"with diff", true, true, false, ""},
		{"custom threshold", true, true, false, ""},
		{"both async", true, true, false, "none"},
		{"both sync", true, true, true, "all"},
		{"only wait for deep", true, true, true, "deep"},
		{"quick sync", true, false, false, ""},
		{"quick async", true, false, false, "none"},
		{"deep sync", false, true, true, "deep"},
		{"deep async", false, true, false, ""},
		{"anonymous default", true, false, false, ""},
		{"anonymous deep", true, false, false, "deep"},
		{"anonymous async", true, false, false, "none"},
		{"anonymous ignores disabled quick", true, false, false, ""},
	} {
		t.Run(mode.name, func(t *testing.T) {
			store := isolate(t)
			guest := strings.HasPrefix(mode.name, "anonymous")
			dir := t.TempDir()
			reviewGit(t, dir, "init", "-b", "main")
			file := filepath.Join(dir, "file.txt")
			if err := os.WriteFile(file, []byte("before\n"), 0600); err != nil {
				t.Fatal(err)
			}
			reviewGit(t, dir, "add", ".")
			reviewGit(t, dir, "commit", "-m", "base")
			if err := os.WriteFile(file, []byte("after\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var submitted struct {
				adversarylabs.WorkspaceSubmission
				Project json.RawMessage `json:"project"`
			}
			polls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if (guest && !adversarylabs.IsAnonymousToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))) || (!guest && r.Header.Get("Authorization") != "Bearer selected-token") {
					t.Error("wrong profile credentials")
				}
				switch r.URL.Path {
				case "/v1/repositories":
					w.Write([]byte(`{"repository_id":"repo","snapshot_id":"snapshot","complete":true}`))
				case "/v1/reviews/workspace":
					if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
						t.Error(err)
					}
					json.NewEncoder(w).Encode(map[string]any{"anonymous": guest, "url": "http://" + r.Host + "/look-closer/org/repo/" + strings.Repeat("a", 64) + "#private", "id": "review", "status": "running", "look_closer_enabled": mode.look, "look_closer_threshold": submitted.LookCloserThreshold, "full_review": map[string]any{"enabled": mode.full, "status": "running"}})
				case "/v1/reviews/workspace/review":
					polls++
					status := "running"
					if mode.wait {
						status = "completed"
					}
					full := map[string]any{"enabled": mode.full, "status": status}
					if !mode.full {
						full["status"] = "disabled"
					}
					if r.URL.Query().Get("results") == "full" {
						full["result"] = map[string]any{"conclusion": "success", "summary": "Deep review finished", "findings": []any{}}
					}
					json.NewEncoder(w).Encode(map[string]any{"anonymous": guest, "url": "http://" + r.Host + "/look-closer/org/repo/" + strings.Repeat("a", 64) + "#private", "id": "review", "status": status, "look_closer_enabled": mode.look, "look_closer_threshold": submitted.LookCloserThreshold, "full_review": full, "early_assessment": map[string]any{"status": "ready", "leads": []map[string]any{{"path": "file.txt", "line": 1, "concern": "Check the new value", "check": "Compare the expected output"}}}})
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			if !guest {
				if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "local"), adversarylabs.Auth{Token: "selected-token"}); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"--api-url", server.URL, "--profile", "local", "review", "--path", dir, "--base", "HEAD", "--poll-interval", "1ms", "--timeout", "5s", "--format", "json"}
			if !mode.full && !guest {
				args = append(args, "--deep-review=false")
			}
			if mode.mode != "" {
				args = append(args, "--wait="+mode.mode)
			}
			if mode.mode == "none" || (!mode.look && mode.mode == "") {
				args = []string{"--api-url", server.URL, "--profile", "local", "review", "--path", dir, "--base", "HEAD", "--format", "json", "--wait=none"}
				if !mode.full {
					args = append(args, "--deep-review=false")
				}
			}
			if !mode.look || mode.name == "anonymous ignores disabled quick" {
				args = append(args, "--look-closer=false")
			}
			if err := store.SetDefaultProject(adversarylabs.AuthKey(server.URL, "local"), "ignored-saved-project"); err != nil {
				t.Fatal(err)
			}
			if mode.name == "custom threshold" {
				args = append(args, "--look-closer-threshold=55")
			}
			if mode.name == "with diff" {
				args = append(args, "--show-diff")
			}
			out, err := execute(t, "", args...)
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if strings.Contains(out, "\n+after\n") != (mode.name == "with diff") {
				t.Fatal("patch display did not respect --show-diff")
			}
			if !strings.Contains(out, "Diff: 1 file, +1/-1 lines, 1 hunk") {
				t.Fatalf("missing diff summary: %s", out)
			}
			lines := strings.Split(strings.TrimSpace(out), "\n")
			var result outputEnvelope[adversarylabs.WorkspaceReview]
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
				t.Fatal(err)
			}
			if (mode.mode == "none" || (!mode.look && mode.mode == "")) && polls != 0 {
				t.Fatal("async review polled for results")
			}
			if mode.look && mode.mode != "none" && (result.Data.Early == nil || len(result.Data.Early.Leads) != 1 || polls == 0) {
				t.Fatalf("did not wait for Look Closer: %s", out)
			}
			wantThreshold := 80
			if mode.name == "custom threshold" {
				wantThreshold = 55
			}
			if mode.look && (submitted.LookCloserThreshold == nil || *submitted.LookCloserThreshold != wantThreshold) {
				t.Fatal("default threshold not submitted")
			}
			if submitted.ReviewScope != "diff" || submitted.FullReview == nil || *submitted.FullReview != mode.full || len(submitted.Project) != 0 || submitted.LookCloser == nil || *submitted.LookCloser != mode.look || !strings.Contains(submitted.Diff, "+after") {
				t.Fatalf("wrong submission: %+v", submitted)
			}
			if guest && (!result.Data.Anonymous || result.Data.URL != "http://"+strings.TrimPrefix(server.URL, "http://")+"/look-closer/org/repo/"+strings.Repeat("a", 64)+"#private") {
				t.Fatal("anonymous link missing")
			}
			if mode.wait && (result.Data.Full == nil || result.Data.Full.Result == nil) {
				t.Fatal("deep review results missing")
			}
		})
	}
}

func TestReviewRejectsDisablingBothStages(t *testing.T) {
	isolate(t)
	_, err := execute(t, "", "review", "--look-closer=false", "--deep-review=false")
	if err == nil || !strings.Contains(err.Error(), "enable at least one") {
		t.Fatalf("%v", err)
	}
}

func TestRepeatedManualReviewsStartFreshAnalysisOfSameSnapshots(t *testing.T) {
	store := isolate(t)
	dir := t.TempDir()
	reviewGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, dir, "add", "file.txt")
	reviewGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var submissions []adversarylabs.WorkspaceSubmission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/repositories":
			w.Write([]byte(`{"repository_id":"repo","snapshot_id":"snapshot","complete":true}`))
		case "/v1/reviews/workspace":
			var submitted adversarylabs.WorkspaceSubmission
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Error(err)
			}
			submissions = append(submissions, submitted)
			w.Write([]byte(`{"id":"review","status":"queued","look_closer_enabled":true,"look_closer_threshold":80,"full_review":{"enabled":false,"status":"disabled"}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "local"), adversarylabs.Auth{Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	args := []string{"--api-url", server.URL, "--profile", "local", "review", "--path", dir, "--base", "HEAD", "--deep-review=false", "--wait=none", "--format=json"}
	for i := 0; i < 2; i++ {
		if out, err := execute(t, "", args...); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if len(submissions) != 2 {
		t.Fatalf("expected two analyses, got %d", len(submissions))
	}
	first, second := submissions[0], submissions[1]
	if first.ReviewID == second.ReviewID || !strings.HasPrefix(first.ReviewID, "cli-") || !strings.HasPrefix(second.ReviewID, "cli-") {
		t.Fatal("manual invocations reused a review identity")
	}
	if first.SnapshotID != second.SnapshotID || first.BaseSnapshotID != second.BaseSnapshotID || first.RepositoryID != second.RepositoryID {
		t.Fatal("fresh analysis changed the content identity")
	}
	if first.ReviewScope != "diff" || second.ReviewScope != "diff" {
		t.Fatal("fresh analysis lost its scope")
	}
}

func TestReviewRejectsInvalidLookCloserThresholdBeforeWork(t *testing.T) {
	for _, value := range []string{"-1", "101", "0.8", "NaN"} {
		t.Run(value, func(t *testing.T) {
			isolate(t)
			_, err := execute(t, "", "review", "--look-closer-threshold="+value)
			if err == nil || !strings.Contains(err.Error(), "look-closer-threshold") {
				t.Fatalf("invalid threshold accepted: %v", err)
			}
		})
	}
}
