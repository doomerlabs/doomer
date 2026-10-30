package watch

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestQuickUploadBatchesDoNotGenerateProgressChatter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var lines []string
	d := uploadProgressDisplay{label: "repo", now: func() time.Time { return now }, log: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	for i := 0; i < 8; i++ {
		d.update("proof_start", 256, 0, 0)
		d.update("transfer_total", 256, 100, 0)
		d.update("transfer", 256, 100, time.Millisecond)
		d.update("transfer_done", 256, 100, time.Millisecond)
		d.update("pack_verify", 256, 0, 0)
		d.update("finalize_start", 0, 0, 0)
		now = now.Add(500 * time.Millisecond)
	}
	if len(lines) != 0 || d.sent != 800 {
		t.Fatalf("quick upload produced chatter or lost progress: %v; sent=%d", lines, d.sent)
	}
	// The same display continues through the current snapshot.
	now = now.Add(2 * time.Second)
	d.update("transfer_total", 1, 100, 0)
	d.update("transfer", 1, 50, time.Second)
	if len(lines) != 1 || !strings.Contains(lines[0], "850 B sent so far") {
		t.Fatal(lines)
	}
}

func TestLongUploadProgressIsThrottledAcrossPhaseChanges(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var lines []string
	d := uploadProgressDisplay{label: "repo", now: func() time.Time { return now }, log: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	d.update("transfer_total", 1, 100, 0)
	now = now.Add(5 * time.Second)
	d.update("transfer", 1, 50, 5*time.Second)
	d.update("transfer_done", 1, 100, 5*time.Second)
	d.update("transfer_done", 1, 100, 5*time.Second)
	d.update("transfer_total", 1, 200, 0)
	now = now.Add(time.Second)
	d.update("transfer", 1, 150, time.Second)
	if len(lines) != 1 || d.sent != 100 {
		t.Fatal(lines, d.sent)
	}
	now = now.Add(4 * time.Second)
	d.update("transfer", 1, 300, 5*time.Second)
	if len(lines) != 2 || !strings.Contains(lines[1], "300 B sent so far") {
		t.Fatal(lines)
	}
}

func TestUploadRetryProgressNeverMovesBackward(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var lines []string
	d := uploadProgressDisplay{label: "repo", now: func() time.Time { return now }, log: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	d.update("transfer_total", 1, 100, 0)
	for _, bytes := range []int{80, 10, 45} {
		now = now.Add(5 * time.Second)
		d.update("transfer", 1, bytes, 0)
	}
	now = now.Add(5 * time.Second)
	d.update("transfer_done", 1, 100, 0)
	d.update("transfer_done", 1, 100, 0)
	d.update("transfer_total", 1, 50, 0)
	now = now.Add(5 * time.Second)
	d.update("transfer", 1, 20, 0)
	want := []string{"80 B", "80 B", "80 B", "100 B", "120 B"}
	if len(lines) != len(want) {
		t.Fatalf("unexpected progress: %v", lines)
	}
	for i, expected := range want {
		if !strings.Contains(lines[i], expected+" sent so far") {
			t.Fatalf("line %d: %s; want %s", i, lines[i], expected)
		}
	}
	if d.sent != 100 {
		t.Fatalf("retry bytes incorrectly counted as complete: %d", d.sent)
	}
}
