package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

func TestWatchListGroupsCheckoutsAndExpandsAsTree(t *testing.T) {
	root := t.TempDir()
	base := t.TempDir()
	repos := []watching.Repository{
		{ID: filepath.Join(base, "project", ".git"), Path: filepath.Join(base, "main"), Explicit: true},
		{ID: filepath.Join(base, "project", ".git"), Path: filepath.Join(base, "feature"), LastActivity: time.Now().Add(-18 * 24 * time.Hour)},
	}
	var output bytes.Buffer
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.SetOut(&output)
	cfg := watching.Config{AllRepositories: true}
	if err := renderWatchRepositories(c, root, cfg, repos, false); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(output.String(), "\n"); lines != 2 {
		t.Fatalf("expected header and one repository row: %s", output.String())
	}
	if strings.Contains(output.String(), "feature") || !strings.Contains(output.String(), "Not running") {
		t.Fatal(output.String())
	}
	output.Reset()
	if err := renderWatchRepositories(c, root, cfg, repos, true); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"├── feature", "└── main", "Inactive · 18 days", "Not running"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("missing %q in %s", text, output.String())
		}
	}
}
