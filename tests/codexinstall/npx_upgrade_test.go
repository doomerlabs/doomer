package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
)

func TestHistoricalPersonalBootstrapIsRecognized(t *testing.T) {
	template, err := os.ReadFile("../../internal/managed/bootstrap-2026.11.9.5.md")
	if err != nil {
		t.Fatal(err)
	}
	text, err := historicalPersonalBootstrap(string(template))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	update, err := managed.InspectBootstrap(path, "personal", "dev")
	if err != nil || update.State != "update_available" {
		t.Fatal(update, err)
	}
}
