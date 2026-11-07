package managed

import (
	"context"
	"fmt"
	"testing"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestNoChangesCompletesWithoutUploadAndDetectsLaterEdits(t *testing.T) {
	store := Store{Root: t.TempDir()}
	handle, _ := NewHandle()
	task := Task{Schema: Protocol, Handle: handle, Stage: "ready"}
	changed := false
	uploads := 0
	engine := Engine{Store: store, Upload: func(context.Context, Task, string, string, sourcesnapshot.Manifest) (string, string, string, error) {
		uploads++
		return "", "", "", fmt.Errorf("unexpected upload")
	}, Capture: func(context.Context, Task) (sourcesnapshot.Capture, error) {
		if !changed {
			return sourcesnapshot.Capture{}, fmt.Errorf("capture: %w", sourcesnapshot.ErrNoChanges)
		}
		return sourcesnapshot.Capture{}, nil
	}}
	if err := engine.Submit(context.Background(), &task, ""); err != nil {
		t.Fatal(err)
	}
	if !task.NoChanges || task.Stage != "no_changes" || task.Revision != 0 || task.Review != nil {
		t.Fatalf("unexpected result: %+v", task)
	}
	if err := engine.Finish(context.Background(), &task, ""); err != nil {
		t.Fatal(err)
	}
	if task.Stage != "complete" || task.Review != nil || uploads != 0 {
		t.Fatal(task.Stage, task.Review, uploads)
	}
	if err := engine.Retry(context.Background(), &task, ""); err == nil || task.Stage != "complete" {
		t.Fatal("retry reopened a completed no-change task", err, task.Stage)
	}
	changed = true
	if err := engine.Finish(context.Background(), &task, ""); err == nil {
		t.Fatal("completed without checking new edits")
	}
	// A new submission must leave the no-change state even if a later capture
	// validation fails, so it cannot accidentally claim the new edits are done.
	if err := engine.Submit(context.Background(), &task, ""); err == nil {
		t.Fatal("accepted an invalid new snapshot")
	}
	if task.NoChanges {
		t.Fatal("retained no-change result after new edits")
	}
}

func TestSetupTaskNeverCapturesOrUploadsAReview(t *testing.T) {
	handle, _ := NewHandle()
	task := Task{Schema: Protocol, Handle: handle, Purpose: "setup", Stage: "ready"}
	engine := Engine{Store: Store{Root: t.TempDir()}, Capture: func(context.Context, Task) (sourcesnapshot.Capture, error) {
		t.Fatal("setup attempted to capture repository content")
		return sourcesnapshot.Capture{}, nil
	}}
	if err := engine.Submit(context.Background(), &task, ""); err != nil {
		t.Fatal(err)
	}
	if err := engine.Finish(context.Background(), &task, ""); err != nil {
		t.Fatal(err)
	}
	if task.Stage != "complete" || task.Submission != nil || task.Review != nil {
		t.Fatalf("unexpected setup state: %+v", task)
	}
}
