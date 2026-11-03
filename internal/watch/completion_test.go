package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanReviewDeliveredOnceWithFullLinkAndOnlyCurrentSnapshot(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	owner := Owner{"session", "turn"}
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
	digest, err := snapshotDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner})
	Write(StatePath(root, dir), State{Owner: owner, Fingerprint: digest, HypothesesSubmitted: true, RequestedAt: time.Now()})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	url := "https://doomer.ai/look-closer/org/repo/digest#review=keep-entire-token"
	feed := Feed{Owner: owner, Fingerprint: digest, ReviewID: "review", URL: url, AssessmentStatus: "ready", Updated: time.Now()}
	run := func() string {
		raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: "Stop", StopActive: true})
		var out strings.Builder
		hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	// Current matching result is delivered even with no leads.
	feed.Fingerprint = digest
	Write(FeedPath(root, dir), feed)
	got := run()
	for _, text := range []string{"no areas to check", "no longer pending", url} {
		if !strings.Contains(got, text) {
			t.Fatalf("missing %q in %s", text, got)
		}
	}
	if got := strings.TrimSpace(run()); got != "{}" {
		t.Fatalf("repeated completion: %s", got)
	}
	// A distinct revision, including a clean one, must be delivered again.
	feed.ReviewID = "review-2"
	Write(FeedPath(root, dir), feed)
	if !strings.Contains(run(), "no longer pending") {
		t.Fatal("second review was suppressed")
	}
}

func TestSavedTaskProvidedByPromptHookOnce(t *testing.T) {
	root, dir := t.TempDir(), repository(t)
	SaveTaskHandoff(root, dir, "session", strings.NewReader("Add pagination. Keep the UI unchanged."))
	raw, _ := json.Marshal(HookInput{Session: "session", Turn: "resume", CWD: dir, Event: "UserPromptSubmit", Prompt: ResumeHandoffPrompt})
	run := func() string {
		var out strings.Builder
		hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	if !strings.Contains(run(), "Add pagination. Keep the UI unchanged.") {
		t.Fatal("missing saved task")
	}
	if strings.Contains(run(), "Add pagination") {
		t.Fatal("task handoff repeated")
	}
	if got := handoffPrompt(root, dir, Owner{"session", "resume"}, ResumeHandoffPrompt); !strings.Contains(got, "Add pagination") {
		t.Fatal("review lost original task")
	}
}
