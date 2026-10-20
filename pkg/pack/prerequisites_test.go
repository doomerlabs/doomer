package pack

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/doomerlabs/doomer/pkg/manifest"
)

func TestGoPrerequisiteSurvivesCheckAndPack(t *testing.T) {
	dir := testProject(t)
	path := filepath.Join(dir, "adversary.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data := append(original, []byte("\nrequires: [go-module-dependencies]\n")...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(Options{Dir: dir}); err != nil {
		t.Fatalf("check: %v", err)
	}
	artifact, err := Create(context.Background(), Options{Dir: dir})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	defer artifact.Close()
	if !bytes.Equal(artifact.AdversaryManifest, data) {
		t.Fatal("packing changed or dropped the prerequisite declaration")
	}
	parsed, err := manifest.Parse(artifact.AdversaryManifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Requires) != 1 || parsed.Requires[0] != "go-module-dependencies" {
		t.Fatalf("packed prerequisites: %v", parsed.Requires)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatal("packing modified the source manifest")
	}
}
