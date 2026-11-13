package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func extraSnapshot(t *testing.T, body, rules string) snapshotMetadata {
	t.Helper()
	snapshot := t.TempDir()
	root := filepath.Join(snapshot, "cli")
	for path, content := range map[string]string{
		"go.mod":                     "module example.test/extras\n\ngo 1.23\n",
		"main.go":                    "package main; func main() {}",
		"tests/codexinstall/main.go": "package main; func main() {}",
		"blargh.txt":                 body,
		"e2e-extra-dependencies.yml": rules,
	} {
		writeImpactFile(t, root, path, content)
	}
	m := impactMetadata(t, snapshot, root)
	if err := prepareRuntimeInputs(context.Background(), snapshot, map[string]string{"cli": root}, &m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "blargh.txt")); err != nil {
		t.Fatal("declared file was pruned", err)
	}
	return m
}

func TestExtraDependenciesAffectOnlyDeclaredScenarios(t *testing.T) {
	rules := "version: 1\nextra_dependencies:\n  blargh.txt: [codex/hook-repair]\n"
	before := extraSnapshot(t, "before", rules)
	after := extraSnapshot(t, "after", rules)
	if before.fingerprint("hook-repair") == after.fingerprint("hook-repair") {
		t.Fatal("declared scenario reused changed input")
	}
	if before.fingerprint("install") != after.fingerprint("install") {
		t.Fatal("extra-only file invalidated unrelated scenario")
	}
	if before.fingerprint("future-scenario") == after.fingerprint("future-scenario") {
		t.Fatal("unknown scenario did not retain all extra inputs")
	}
	if before.DependencyAnalysis == nil || before.DependencyAnalysis.Mode != "shadow" || before.DependencyAnalysis.Error != "" {
		t.Fatalf("missing shadow analysis: %+v", before.DependencyAnalysis)
	}
	// Explainability cannot authorize a hit or change a previously valid key.
	after.DependencyAnalysis = &dependencyAnalysis{Mode: "shadow", Error: "analysis unavailable"}
	if before.fingerprint("install") != after.fingerprint("install") {
		t.Fatal("shadow diagnostics changed the actual cache decision")
	}
}

func TestSignedCacheRespectsExtraDependencies(t *testing.T) {
	rules := "version: 1\nextra_dependencies:\n  blargh.txt: [codex/hook-repair]\n"
	before := extraSnapshot(t, "before", rules)
	after := extraSnapshot(t, "after", rules)
	keys := testCacheKey(t)
	store := &memoryCache{objects: map[string][]byte{}}
	path := filepath.Join(t.TempDir(), "report.json")
	writeCacheReport(t, path, true)
	if err := publishCachedResult(context.Background(), store, before, "install", path, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := reuseCachedResult(context.Background(), store, after, "install", filepath.Join(t.TempDir(), "hit.json"), keys); err != nil {
		t.Fatal("unrelated signed result should remain reusable", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var repaird report
	if err := json.Unmarshal(raw, &repaird); err != nil {
		t.Fatal(err)
	}
	repaird.Scenario = "hook-repair"
	raw, err = json.Marshal(repaird)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishCachedResult(context.Background(), store, before, "hook-repair", path, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := reuseCachedResult(context.Background(), store, after, "hook-repair", filepath.Join(t.TempDir(), "miss.json"), keys); err == nil {
		t.Fatal("changed extra dependency reused a signed result")
	}
	writeCacheReport(t, path, true)
	// The local/CI compatibility lookup uses this same fingerprint path.
	after.OS, after.Arch = "linux", "amd64"
	before.OS, before.Arch = "linux", "amd64"
	if err := publishCachedResult(context.Background(), store, before, "install", path, time.Now()); err != nil {
		t.Fatal(err)
	}
	after.Environment = "ci"
	hit := false
	for _, candidate := range cacheLookupEnvironments(after) {
		if err := reuseCachedResult(context.Background(), store, candidate, "install", filepath.Join(t.TempDir(), "ci-hit.json"), keys); err == nil {
			hit = true
		}
	}
	if !hit {
		t.Fatal("CI could not reuse the matching local result")
	}
}

func TestExtraManifestRejectsUnknownInput(t *testing.T) {
	root := t.TempDir()
	writeImpactFile(t, root, "e2e-extra-dependencies.yml", "version: 1\nextra_dependencies:\n  missing.txt: [codex/install]\n")
	_, err := extraDependencyFiles(root, filepath.Dir(root), testCacheMetadata())
	if err == nil {
		t.Fatal("missing extra input silently accepted")
	}
}
