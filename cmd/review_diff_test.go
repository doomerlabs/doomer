package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestReviewDiffStats(t *testing.T) {
	for _, tc := range []struct {
		name, diff string
		want       reviewDiffStats
	}{
		{"empty", "", reviewDiffStats{}},
		{"replacement", "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n", reviewDiffStats{1, 1, 1, 1}},
		{"content resembling headers", "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n---old content\n+++new content\n@@ -5,0 +6 @@\n+another\n\\ No newline at end of file\n", reviewDiffStats{1, 2, 1, 2}},
		{"mode and binary changes", "diff --git a/tool b/tool\nold mode 100644\nnew mode 100755\ndiff --git a/image b/image\nBinary files a/image and b/image differ\n", reviewDiffStats{2, 0, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeReviewDiff(tc.diff); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
	if got := (reviewDiffStats{1, 4, 2, 1}).String(); got != "Diff: 1 file, +4/-2 lines, 1 hunk" {
		t.Fatal(got)
	}
}

func TestReviewJSONIncludesDiffStats(t *testing.T) {
	var output bytes.Buffer
	stats := reviewDiffStats{2, 10, 3, 4}
	if err := writeWorkspaceReview(&output, "json", adversarylabs.WorkspaceReview{ID: "review", Status: "running"}, "https://example.test/review", &stats); err != nil {
		t.Fatal(err)
	}
	var result outputEnvelope[struct {
		Diff reviewDiffStats `json:"diff"`
	}]
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Diff != stats {
		t.Fatalf("stats missing: %s", output.String())
	}
}
