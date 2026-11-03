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
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary")
	}
	dir, root := repository(t), t.TempDir()
	if err := os.Symlink("file.txt", filepath.Join(dir, "source-link")); err != nil {
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
	if !strings.Contains(first, "unsupported source symlink") || !strings.Contains(first, "could not start a review") {
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
