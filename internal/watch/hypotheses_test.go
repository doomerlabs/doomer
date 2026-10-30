package watch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHypothesesAreBoundToExactEditAndSingleUse(t *testing.T) {
	dir := repository(t)
	root := t.TempDir()
	owner := Owner{Session: "s", Turn: "t"}
	digest, err := snapshotDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	state := State{Owner: owner, Fingerprint: digest, RequestedAt: time.Now()}
	if err = Write(StatePath(root, dir), state); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Directories: []string{dir}, Profile: "local", API: "http://localhost:3000/api"}
	text, err := requestHypotheses(root, dir, cfg, state)
	if err != nil || !strings.Contains(text, "watch hypotheses") {
		t.Fatalf("request: %q %v", text, err)
	}
	Read(StatePath(root, dir), &state)
	data := `[{"path":"file.txt","line":1,"hypothesis":"Could this change lose content?","check":"Compare the before and after values."}]`
	if err = SubmitHypotheses(context.Background(), root, state.QuestionRequest, cfg, strings.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	var saved State
	Read(StatePath(root, dir), &saved)
	if !saved.HypothesesSubmitted || len(saved.Hypotheses) != 1 {
		t.Fatal("questions not saved")
	}
	if err = SubmitHypotheses(context.Background(), root, state.QuestionRequest, cfg, strings.NewReader(data)); err == nil {
		t.Fatal("request replay accepted")
	}
	if awaitingHypotheses(root, dir) {
		t.Fatal("watcher still waiting after submission")
	}
}
func TestHypothesisWaitExpiresWithoutBlockingWatcher(t *testing.T) {
	root := t.TempDir()
	path := "/repo"
	Write(StatePath(root, path), State{RequestedAt: time.Now().Add(-hypothesisGrace - time.Second)})
	if awaitingHypotheses(root, path) {
		t.Fatal("expired request blocks watcher")
	}
}

func TestHypothesisHelperOnlyEmitsAndTrustedHookRecords(t *testing.T) {
	dir := repository(t)
	root := t.TempDir()
	owner := Owner{Session: "s", Turn: "t"}
	digest, err := snapshotDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	state := State{Owner: owner, Fingerprint: digest, RequestedAt: time.Now()}
	Write(StatePath(root, dir), state)
	cfg := Config{Directories: []string{dir}, Profile: "local", API: "http://localhost:3000/api"}
	if _, err = requestHypotheses(root, dir, cfg, state); err != nil {
		t.Fatal(err)
	}
	Read(StatePath(root, dir), &state)
	var output strings.Builder
	if err = EmitHypotheses(context.Background(), root, state.QuestionRequest, cfg, strings.NewReader(`[]`), &output); err != nil {
		t.Fatal(err)
	}
	var saved State
	Read(StatePath(root, dir), &saved)
	if saved.HypothesesSubmitted {
		t.Fatal("helper wrote integration state")
	}
	raw, _ := json.Marshal(map[string]any{"output": output.String()})
	if got := recordHypothesesOutput(context.Background(), root, cfg, Owner{Session: "other", Turn: "t"}, raw); got != "" {
		t.Fatal("another conversation submitted the request")
	}
	if got := recordHypothesesOutput(context.Background(), root, cfg, owner, raw); got != dir {
		t.Fatal("trusted hook did not record output")
	}
	Read(StatePath(root, dir), &saved)
	if !saved.HypothesesSubmitted {
		t.Fatal("hook did not save submission")
	}
}
