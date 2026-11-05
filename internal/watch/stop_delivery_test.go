package watch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestCompletedReviewDeliveredAfterCommit(t *testing.T) {
	repo, root := repository(t), t.TempDir()
	owner := Owner{"session", "turn"}
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600)
	fp, err := snapshotDigest(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{repo}})
	Write(StatePath(root, repo), State{Owner: owner, Fingerprint: fp, HypothesesSubmitted: true, RequestedAt: time.Now()})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	Write(FeedPath(root, repo), Feed{Owner: owner, Fingerprint: fp, ReviewID: "review", URL: "https://example.com/review", AssessmentStatus: "ready", Updated: time.Now()})
	run := func(event string) string {
		raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: repo, Event: event, Tool: "Bash", ToolID: "commit", Input: json.RawMessage(`{"command":"git add . && git commit -m change"}`)})
		var out strings.Builder
		hookForTest(context.Background(), root, Config{Directories: []string{repo}}, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	run("PreToolUse")
	for _, args := range [][]string{{"add", "."}, {"-c", "core.hooksPath=/dev/null", "commit", "-m", "change"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %s %v", out, err)
		}
	}
	post := run("PostToolUse")
	stop := run("Stop")
	var state State
	Read(StatePath(root, repo), &state)
	if !state.Ambiguous || agentReviewRequested(root, repo, state.Fingerprint, time.Time{}) {
		t.Fatal("delivery restored ownership of an ambiguous edit")
	}
	if !strings.Contains(post, "earlier snapshot") || strings.Contains(stop, "https://example.com/review") {
		t.Fatalf("snapshot labeling or deduplication failed: post=%s stop=%s", post, stop)
	}
	if !strings.Contains(post+stop, "https://example.com/review") {
		t.Fatal("completed review lost after commit")
	}
}

func TestCommittedReviewedContentIsStillReportedComplete(t *testing.T) {
	repo, root := repository(t), t.TempDir()
	owner := Owner{"session", "turn"}
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600)
	capture, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Workspace: true, Path: repo})
	fp := capture.Digest
	if err != nil {
		t.Fatal(err)
	}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{repo}})
	Write(StatePath(root, repo), State{Owner: owner, Fingerprint: fp, HypothesesSubmitted: true, RequestedAt: time.Now()})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	manifest, err := sourcesnapshot.DecodeWorkspace(capture.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	Write(FeedPath(root, repo), Feed{BaseRef: manifest.BaseRef, TargetTreeHash: capture.TargetTreeHash, Owner: owner, Fingerprint: fp, ReviewID: "review", URL: "https://example.com/review", AssessmentStatus: "ready", Updated: time.Now()})
	run := func(event string) string {
		raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: repo, Event: event, Tool: "Bash", ToolID: "commit", Input: json.RawMessage(`{"command":"git add . && git commit -m change"}`)})
		var out strings.Builder
		hookForTest(context.Background(), root, Config{Directories: []string{repo}}, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	run("PreToolUse")
	for _, args := range [][]string{{"add", "."}, {"-c", "core.hooksPath=/dev/null", "commit", "-m", "change"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %s %v", out, err)
		}
	}
	post := run("PostToolUse")
	stop := run("Stop")
	var state State
	Read(StatePath(root, repo), &state)
	if !state.Ambiguous || agentReviewRequested(root, repo, state.Fingerprint, time.Time{}) {
		t.Fatal("delivery restored ownership of an ambiguous edit")
	}
	if strings.Contains(post, "earlier snapshot") || !strings.Contains(post, "latest edit") || strings.Contains(stop, "https://example.com/review") {
		t.Fatalf("snapshot labeling or deduplication failed: post=%s stop=%s", post, stop)
	}
	if !strings.Contains(post+stop, "https://example.com/review") {
		t.Fatal("completed review lost after commit")
	}
}

func TestStopFromParentDeliversAfterVisitingTwoRepositories(t *testing.T) {
	repo, other, parent, root := repository(t), repository(t), t.TempDir(), t.TempDir()
	owner := Owner{"session", "turn"}
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600)
	fp, _ := snapshotDigest(context.Background(), repo)
	Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{other, repo}})
	Write(StatePath(root, repo), State{Owner: owner, Fingerprint: fp, HypothesesSubmitted: true, RequestedAt: time.Now()})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	Write(FeedPath(root, repo), Feed{Owner: owner, Fingerprint: fp, ReviewID: "review", URL: "https://example.com/review", AssessmentStatus: "ready", Updated: time.Now()})
	raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: parent, Event: "Stop"})
	var out strings.Builder
	hookForTest(context.Background(), root, Config{Directories: []string{repo, other}}, strings.NewReader(string(raw)), &out)
	if !strings.Contains(out.String(), "https://example.com/review") {
		t.Fatalf("completed review lost when Stop has two visited repos: %s", out.String())
	}
}

func TestStopFanoutOnlyDeliversOwnedApprovedResults(t *testing.T) {
	for _, excluded := range []string{"different-session", "different-turn", "unapproved", "stale"} {
		t.Run(excluded, func(t *testing.T) {
			good, other, parent, root := repository(t), repository(t), t.TempDir(), t.TempDir()
			owner := Owner{"session", "turn"}
			Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{other, good, good}})
			Write(filepath.Join(root, "heartbeat.json"), time.Now())
			for _, repo := range []string{good, other} {
				os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600)
				fp, err := snapshotDigest(context.Background(), repo)
				if err != nil {
					t.Fatal(err)
				}
				feed := Feed{Owner: owner, Fingerprint: fp, ReviewID: filepath.Base(repo), URL: "https://example.com/" + filepath.Base(repo), AssessmentStatus: "ready", Updated: time.Now()}
				if repo == other {
					switch excluded {
					case "different-session":
						feed.Owner.Session = "another-session"
					case "different-turn":
						feed.Owner.Turn = "another-turn"
					case "stale":
						feed.Updated = time.Now().Add(-25 * time.Hour)
					}
				}
				Write(StatePath(root, repo), State{Owner: owner, Fingerprint: fp, HypothesesSubmitted: true, RequestedAt: time.Now()})
				Write(FeedPath(root, repo), feed)
			}
			dirs := []string{good, other}
			if excluded == "unapproved" {
				dirs = []string{good}
			}
			raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: parent, Event: "Stop"})
			var out strings.Builder
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			hookForTest(ctx, root, Config{Directories: dirs}, strings.NewReader(string(raw)), &out)
			if strings.Count(out.String(), "https://example.com/"+filepath.Base(good)) != 1 || strings.Contains(out.String(), "https://example.com/"+filepath.Base(other)) {
				t.Fatal("wrong or duplicate review delivered", out.String())
			}
		})
	}
}

func TestStopFanoutCombinesReadyReviews(t *testing.T) {
	root, parent := t.TempDir(), t.TempDir()
	owner := Owner{"session", "turn"}
	repos := []string{repository(t), repository(t)}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: repos})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	for i, repo := range repos {
		os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600)
		fp, _ := snapshotDigest(context.Background(), repo)
		Write(StatePath(root, repo), State{Owner: owner, Fingerprint: fp, HypothesesSubmitted: true, RequestedAt: time.Now()})
		Write(FeedPath(root, repo), Feed{Owner: owner, Fingerprint: fp, ReviewID: repo, URL: []string{"https://example.com/first", "https://example.com/second"}[i], AssessmentStatus: "ready", Updated: time.Now()})
	}
	raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: parent, Event: "Stop"})
	var out strings.Builder
	hookForTest(context.Background(), root, Config{Directories: repos}, strings.NewReader(string(raw)), &out)
	for _, url := range []string{"https://example.com/first", "https://example.com/second"} {
		if strings.Count(out.String(), url) != 1 {
			t.Fatal("missing or duplicated result", out.String())
		}
	}
}
