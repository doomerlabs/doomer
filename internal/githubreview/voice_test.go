package githubreview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/review"
)

func TestResolveVoiceDefaultAndOverride(t *testing.T) {
	prompt, info := ResolveVoice("")
	if info.Source != "cli_default" || !strings.Contains(prompt, "Doomer") {
		t.Fatalf("%s %q", info.Source, prompt[:min(40, len(prompt))])
	}
	// Prefer agent/voice.md over legacy VOICE.md
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VOICE.md"), []byte("legacy VOICE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "voice.md"), []byte("Custom voice for Acme"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt, info = ResolveVoice(dir)
	if info.Source != "package" || info.Path != filepath.Join("agent", "voice.md") || prompt != "Custom voice for Acme" {
		t.Fatalf("%#v %q", info, prompt)
	}
	// Package root wins over target root
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "voice.md"), []byte("target voice"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt, info = ResolveVoice(dir, target)
	if prompt != "Custom voice for Acme" {
		t.Fatalf("package should win: %q %#v", prompt, info)
	}
}

func TestTemplateBodyMarker(t *testing.T) {
	line := 2
	body := TemplateBody("go-cli", review.Finding{
		ID: "id1", Title: "Stuck work blocks new submissions", Severity: "high",
		Summary:        strings.Repeat("The full diagnostic history is long. ", 30),
		Recommendation: "Reset stale generating rows: either use a timeout or add a periodic reaper with detailed handling for every worker path, accounting for missing builders, cleared directories, worker restarts, retry state, terminal failures, and old queue entries so every possible transition is covered before a later submission is accepted.",
	}, "f.go", &line)
	if !strings.Contains(body, "adversary-review:v1") || !strings.Contains(body, "f.go:2") {
		t.Fatal(body)
	}
	visible := strings.TrimSpace(stripReviewMarker(body))
	if visible != "Stuck work blocks new submissions. Reset stale generating rows." {
		t.Fatalf("fallback is not a short comment: %q", visible)
	}
	for _, unwanted := range []string{"high", "go-cli", "Where:", "Recommendation:", "f.go"} {
		if strings.Contains(visible, unwanted) {
			t.Fatalf("fallback leaked %q: %q", unwanted, visible)
		}
	}
}

func TestTemplateBodyClipsLongLead(t *testing.T) {
	for _, tc := range []struct {
		name, summary, wantStart string
	}{
		{name: "long summary", summary: strings.Repeat("summary ", 80), wantStart: "summary"},
		{name: "missing summary", wantStart: "title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := TemplateBody("go-cli", review.Finding{
				ID:             "id1",
				Title:          strings.Repeat("title ", 80),
				Summary:        tc.summary,
				Recommendation: strings.Repeat("fix ", 20),
			}, "f.go", nil)
			visible := strings.TrimSpace(stripReviewMarker(body))
			if words := len(strings.Fields(visible)); words > 50 {
				t.Fatalf("fallback has %d words: %q", words, visible)
			}
			if !strings.HasPrefix(visible, tc.wantStart) {
				t.Fatalf("fallback used wrong lead: %q", visible)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
