package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCaptureFailureIsDeliveredOnceWithoutReviewState(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pointer"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := Owner{"chat", "turn"}
	if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: "PostToolUse", Tool: "apply_patch", ToolID: "edit"})
	run := func() string {
		var out strings.Builder
		hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	first := run()
	if !strings.Contains(first, "local snapshot capture failed") || !strings.Contains(first, "could not start a review") {
		t.Fatal(first)
	}
	if second := strings.TrimSpace(run()); second != "{}" {
		t.Fatal("repeated capture failure", second)
	}
	var turn Turn
	if err := Read(TurnPath(root, owner.Session), &turn); err != nil {
		t.Fatal(err)
	}
	other := captureFailureNotice(root, TurnPath(root, owner.Session), Owner{"another", "turn"}, context.DeadlineExceeded)
	if other != "" {
		t.Fatal("reported transient or unrelated failure")
	}
}

func TestUnresolvedRepositoryNoticeIsScopedToOperation(t *testing.T) {
	root := t.TempDir()
	owner := Owner{"chat", "turn"}
	turnFile := TurnPath(root, owner.Session)
	if err := Write(turnFile, Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	message := captureFailureNotice(root, turnFile, owner, errCaptureRepository)
	for _, text := range []string{"this tool operation", "review-exempt skill maintenance", "For actual repository edits", "no later completed review"} {
		if !strings.Contains(message, text) {
			t.Errorf("notice missing %q: %s", text, message)
		}
	}
	if repeated := captureFailureNotice(root, turnFile, owner, errCaptureRepository); repeated != "" {
		t.Fatal("repeated unresolved repository notice", repeated)
	}
}

func TestSymlinksDoNotEmitCaptureFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary")
	}
	dir, root := repository(t), t.TempDir()
	if err := os.Symlink("missing", filepath.Join(dir, "source-link")); err != nil {
		t.Fatal(err)
	}
	owner := Owner{"chat", "turn"}
	if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: "PostToolUse", Tool: "apply_patch", ToolID: "edit"})
	var out strings.Builder
	hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
	if strings.TrimSpace(out.String()) != "{}" {
		t.Fatalf("unexpected hook message: %s", out.String())
	}
	digest, err := snapshotDigest(context.Background(), dir)
	if err != nil || digest != "clean" {
		t.Fatalf("symlink blocked clean snapshot: %q, %v", digest, err)
	}
}
