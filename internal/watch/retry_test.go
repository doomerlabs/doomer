package watch

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type recoverableUploadClient struct {
	fakeClient
	root    string
	uploads int
}

func (c *recoverableUploadClient) UploadWorkspaceTree(ctx context.Context, token, repo string, entries []sourcesnapshot.Entry, blobs map[string][]byte) (string, string, error) {
	c.uploads++
	if c.uploads == 1 {
		if err := Write(TurnPath(c.root, "test-chat"), Turn{Owner: Owner{Session: "test-chat", Turn: "next-turn"}}); err != nil {
			return "", "", err
		}
		return "", "", errors.New("temporary upload failure")
	}
	return c.fakeClient.UploadWorkspaceTree(ctx, token, repo, entries, blobs)
}

func TestWatcherRetriesFailedUploadWithoutAnotherEdit(t *testing.T) {
	t.Parallel()
	path := repository(t)
	if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("updated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	client := &recoverableUploadClient{root: root}
	r := Runner{Root: root, Config: Config{API: "https://example.com/api"}, Client: client, Output: io.Discard, Interval: time.Minute}
	requestTestAgentReview(t, &r, path)
	var original State
	if err := Read(StatePath(root, path), &original); err != nil {
		t.Fatal(err)
	}
	// Use the helper's actual owner when advancing the live conversation.
	original.Owner = Owner{Session: "test-chat", Turn: "edit-turn"}
	original.Prompt = "Update the file"
	original.HypothesesSubmitted = true
	if err := Write(StatePath(root, path), original); err != nil {
		t.Fatal(err)
	}
	if err := Write(TurnPath(root, "test-chat"), Turn{Owner: original.Owner}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan struct{})
	var runErr error
	go func() { runErr = r.Run(ctx, path, false); close(done) }()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			t.Fatalf("watcher exited before recovering: %v", runErr)
		case <-ticker.C:
			var feed Feed
			if Read(FeedPath(root, path), &feed) != nil || feed.AssessmentStatus != "ready" {
				continue
			}
			if feed.Owner != original.Owner {
				t.Fatal("recovered review sent to the wrong conversation")
			}
			cancel()
			<-done
			if client.starts != 1 || client.uploads < 2 {
				t.Fatal("failed upload was not retried")
			}
			if client.input.UserRequest == nil || client.input.UserRequest.TurnID != original.Owner.Turn {
				t.Fatal("retried submission lost the original intent")
			}
			return
		}
	}
}

func TestFailedRequestCanRetryAfterConversationMovesOn(t *testing.T) {
	root, path := t.TempDir(), "/repo"
	original := State{Owner: Owner{Session: "chat", Turn: "edit"}, Fingerprint: "contents", RequestedAt: time.Now()}
	if err := Write(StatePath(root, path), original); err != nil {
		t.Fatal(err)
	}
	if err := Write(TurnPath(root, "chat"), Turn{Owner: Owner{Session: "chat", Turn: "next"}}); err != nil {
		t.Fatal(err)
	}
	if agentReviewRequested(root, path, "contents", original.RequestedAt.Add(-time.Second)) {
		t.Fatal("a previous turn should not start an unrequested new review")
	}
	if !retryReviewRequested(root, path, "contents", original) {
		t.Fatal("a failed authorized upload must remain retryable without another agent edit")
	}
	var current State
	if err := Read(StatePath(root, path), &current); err != nil {
		t.Fatal(err)
	}
	if current.Owner != original.Owner {
		t.Fatal("retry was reassigned to a different turn")
	}
}

func TestRetryCannotFollowAnotherEditOrConversation(t *testing.T) {
	root, path := t.TempDir(), "/repo"
	original := State{Owner: Owner{Session: "chat", Turn: "edit"}, Fingerprint: "contents", RequestedAt: time.Now()}
	cases := []struct {
		name   string
		modify func(*State)
		digest string
	}{
		{"different contents", func(s *State) {}, "changed"},
		{"different conversation", func(s *State) { s.Owner.Session = "other" }, "contents"},
		{"different turn", func(s *State) { s.Owner.Turn = "other" }, "contents"},
		{"new request", func(s *State) { s.RequestedAt = s.RequestedAt.Add(time.Second) }, "contents"},
		{"edit in progress", func(s *State) { s.Active = map[string]Operation{"edit": {}} }, "contents"},
		{"ambiguous owner", func(s *State) { s.Ambiguous = true }, "contents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := original
			tc.modify(&current)
			if err := Write(StatePath(root, path), current); err != nil {
				t.Fatal(err)
			}
			if retryReviewRequested(root, path, tc.digest, original) {
				t.Fatal("superseded or unsafe retry accepted")
			}
		})
	}
}
