package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func automaticFixture(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	snapshot := t.TempDir()
	root := filepath.Join(snapshot, "cli")
	base := map[string]string{
		"go.mod":                     "module example.test/scenarios\n\ngo 1.23\n",
		"main.go":                    "package main; func main() { runWorkerContext(config{}) }",
		"tests/codexinstall/main.go": "package main; func main() { runWorkerContext(config{}) }",
		"tests/codexinstall/scenarios.go": `package main
type config struct { Scenario string }; type journey struct { c config }
func (j *journey) afterInstall() error {
 switch j.c.Scenario { case "upgrade": return j.upgrade(); case "install": return j.install() }; return nil
}`,
		"tests/codexinstall/worker.go":  "package main; func runWorkerContext(c config) { j:=journey{c:c}; _=j.afterInstall() }",
		"tests/codexinstall/blargh.go":  "package main; func blargh() int { return 1 }; func (j *journey) upgrade() error { _=blargh(); _=shared(); return nil }",
		"tests/codexinstall/install.go": "package main; func (j *journey) install() error { _=shared(); return nil }",
		"tests/codexinstall/shared.go":  "package main; func shared() int { return 2 }",
	}
	// The outer command is irrelevant to these fixture-only graph tests.
	base["main.go"] = "package main; func main() {}"
	for path, text := range files {
		base[path] = text
	}
	for path, text := range base {
		writeImpactFile(t, root, path, text)
	}
	return root, snapshot
}

func automaticMetadata(t *testing.T, root, snapshot string) snapshotMetadata {
	t.Helper()
	m := impactMetadata(t, snapshot, root)
	var err error
	m.ScenarioFiles = nil
	m.ScenarioHashes, err = scenarioBodyInputs(context.Background(), root, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAutomaticScenarioMutationAndNewCallEdge(t *testing.T) {
	root, snapshot := automaticFixture(t, nil)
	before := automaticMetadata(t, root, snapshot)
	if len(before.ScenarioHashes) == 0 {
		t.Fatal("automatic projection was disabled")
	}
	writeImpactFile(t, root, "tests/codexinstall/blargh.go", "package main; func blargh() int { return 99 }; func (j *journey) upgrade() error { _=blargh(); _=shared(); return nil }")
	after := automaticMetadata(t, root, snapshot)
	if before.fingerprint("upgrade") == after.fingerprint("upgrade") {
		t.Fatal("dependent scenario reused changed helper")
	}
	if before.fingerprint("install") != after.fingerprint("install") {
		t.Fatal("unrelated scenario reran")
	}
	writeImpactFile(t, root, "tests/codexinstall/install.go", "package main; func (j *journey) install() error { _=blargh(); _=shared(); return nil }")
	newEdge := automaticMetadata(t, root, snapshot)
	writeImpactFile(t, root, "tests/codexinstall/blargh.go", "package main; func blargh() int { return 100 }; func (j *journey) upgrade() error { _=blargh(); _=shared(); return nil }")
	changed := automaticMetadata(t, root, snapshot)
	if newEdge.fingerprint("install") == changed.fingerprint("install") {
		t.Fatal("new call edge was not automatically discovered")
	}
	if before.fingerprint("unknown") == after.fingerprint("unknown") {
		t.Fatal("unknown scenario skipped changed inputs")
	}
}

func TestAutomaticScenarioSharedAndDispatcherChanges(t *testing.T) {
	root, snapshot := automaticFixture(t, nil)
	before := automaticMetadata(t, root, snapshot)
	writeImpactFile(t, root, "tests/codexinstall/shared.go", "package main; func shared() int { return 42 }")
	after := automaticMetadata(t, root, snapshot)
	for _, name := range []string{"install", "upgrade"} {
		if before.fingerprint(name) == after.fingerprint(name) {
			t.Fatal("shared edit did not invalidate", name)
		}
	}
	before = after
	writeImpactFile(t, root, "tests/codexinstall/scenarios.go", `package main
type config struct { Scenario string }; type journey struct { c config }
func (j *journey) afterInstall() error {
 switch j.c.Scenario { case "upgrade": _=123; return j.upgrade(); case "install": return j.install() }; return nil
}`)
	after = automaticMetadata(t, root, snapshot)
	if before.fingerprint("upgrade") == after.fingerprint("upgrade") {
		t.Fatal("dispatcher branch edit ignored")
	}
	if before.fingerprint("install") != after.fingerprint("install") {
		t.Fatal("unrelated dispatcher branch edit invalidated install")
	}
}

func TestAutomaticScenarioUncertaintyFallsBack(t *testing.T) {
	for name, source := range map[string]string{
		"alias":          "package main; func mutate(j *journey) { alias:=&j.c; alias.Scenario=\"install\" }",
		"pointer-write":  "package main; func mutate(c *config) { *c=config{} }",
		"external-write": "package main; import \"encoding/json\"; func mutate(c *config) { _=json.Unmarshal([]byte(`{}`),c) }",
		"reflection":     "package main; import \"reflect\"; func mutate(j *journey) { _=reflect.ValueOf(j) }",
		"conversion":     "package main; type other struct { c config }; func mutate(x other) { j:=journey(x); _=j.afterInstall() }",
		"copy":           "package main; func mutate(c config) { copy:=c; copy.Scenario=\"install\" }",
	} {
		t.Run(name, func(t *testing.T) {
			root, snapshot := automaticFixture(t, map[string]string{"tests/codexinstall/mutation.go": source})
			m := automaticMetadata(t, root, snapshot)
			if len(m.ScenarioHashes) != 0 {
				t.Fatal("uncertain identity permitted selective reuse")
			}
		})
	}
}

func TestAutomaticScenarioDispatchBindingAndFallthrough(t *testing.T) {
	for name, dispatch := range map[string]string{
		"shadowed-receiver": `{ j:=struct{c config}{}; switch j.c.Scenario { case "upgrade": return jUpgrade() } }; return nil`,
		"fallthrough":       `switch j.c.Scenario { case "install": fallthrough; case "upgrade": return j.upgrade() }; return nil`,
	} {
		t.Run(name, func(t *testing.T) {
			source := "package main; type config struct { Scenario string }; type journey struct { c config }; func jUpgrade() error { return nil }; func (j *journey) afterInstall() error { " + dispatch + " }"
			root, snapshot := automaticFixture(t, map[string]string{"tests/codexinstall/scenarios.go": source})
			if len(automaticMetadata(t, root, snapshot).ScenarioHashes) != 0 {
				t.Fatal("uncertain dispatcher binding permitted projection")
			}
		})
	}
}

func TestAutomaticScenarioRawStringCases(t *testing.T) {
	root, snapshot := automaticFixture(t, nil)
	path := filepath.Join(root, "tests/codexinstall/scenarios.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), `"upgrade"`, "`upgrade`")), 0600); err != nil {
		t.Fatal(err)
	}
	before := automaticMetadata(t, root, snapshot)
	writeImpactFile(t, root, "tests/codexinstall/blargh.go", "package main; func blargh() int { return 456 }; func (j *journey) upgrade() error { _=blargh(); _=shared(); return nil }")
	after := automaticMetadata(t, root, snapshot)
	if before.fingerprint("upgrade") == after.fingerprint("upgrade") || before.fingerprint("install") != after.fingerprint("install") {
		t.Fatal("raw-string case changed dependency selection")
	}
}

func TestRealRunnerAutomaticScenarioProjections(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	hashes, err := scenarioBodyInputs(context.Background(), root, filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) == 0 {
		t.Fatal("real scenario projection unexpectedly disabled")
	}
	var different bool
	for _, values := range hashes {
		different = different || values["install"] != values["upgrade"]
	}
	if !different {
		t.Fatal("real install and upgrade have identical projections")
	}
}

func TestAutomaticScenarioSignedReuseAcrossLocalAndCI(t *testing.T) {
	root, snapshot := automaticFixture(t, nil)
	before := automaticMetadata(t, root, snapshot)
	before.OS, before.Arch = "linux", "amd64"
	keys := testCacheKey(t)
	store := &memoryCache{objects: map[string][]byte{}}
	for _, scenario := range []string{"install", "upgrade"} {
		path := filepath.Join(t.TempDir(), "report.json")
		writeCacheReport(t, path, true)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var r report
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		r.Scenario = scenario
		raw, err = json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := publishCachedResult(context.Background(), store, before, scenario, path, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	writeImpactFile(t, root, "tests/codexinstall/blargh.go", "package main; func blargh() int { return 999 }; func (j *journey) upgrade() error { _=blargh(); _=shared(); return nil }")
	after := automaticMetadata(t, root, snapshot)
	after.OS, after.Arch = "linux", "amd64"
	for _, environment := range []string{"local", "ci"} {
		after.Environment = environment
		for _, scenario := range []string{"install", "upgrade"} {
			hit := false
			for _, candidate := range cacheLookupEnvironments(after) {
				if err := reuseCachedResult(context.Background(), store, candidate, scenario, filepath.Join(t.TempDir(), "result.json"), keys); err == nil {
					hit = true
				}
			}
			if hit != (scenario == "install") {
				t.Fatalf("%s/%s: unexpected reuse=%t", environment, scenario, hit)
			}
		}
	}
}

func TestAutomaticScenarioIndirectReferenceStaysShared(t *testing.T) {
	root, snapshot := automaticFixture(t, map[string]string{"tests/codexinstall/callback.go": "package main; var callback = blargh"})
	before := automaticMetadata(t, root, snapshot)
	writeImpactFile(t, root, "tests/codexinstall/blargh.go", "package main; func blargh() int { return 888 }; func (j *journey) upgrade() error { _=blargh(); _=shared(); return nil }")
	after := automaticMetadata(t, root, snapshot)
	if before.fingerprint("install") == after.fingerprint("install") {
		t.Fatal("escaped helper incorrectly excluded from install")
	}
}

func TestExtraDependencyOverridesAutomaticBodyExclusion(t *testing.T) {
	root, snapshot := automaticFixture(t, nil)
	before := automaticMetadata(t, root, snapshot)
	before.ExtraFiles = map[string][]string{"install": {"cli/tests/codexinstall/blargh.go"}}
	writeImpactFile(t, root, "tests/codexinstall/blargh.go", "package main; func blargh() int { return 777 }; func (j *journey) upgrade() error { _=blargh(); _=shared(); return nil }")
	after := automaticMetadata(t, root, snapshot)
	after.ExtraFiles = before.ExtraFiles
	if before.fingerprint("install") == after.fingerprint("install") {
		t.Fatal("manual extra was narrowed by automatic analysis")
	}
}

func TestRealRepositoryMutationSelectsOnlyAffectedScenarios(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := t.TempDir()
	entries, err := copySource(context.Background(), root, snapshot, "cli", "")
	if err != nil {
		t.Fatal(err)
	}
	captured := filepath.Join(snapshot, "cli")
	before := testCacheMetadata()
	before.Files = entries
	if err := prepareRuntimeInputs(context.Background(), snapshot, map[string]string{"cli": captured}, &before); err != nil {
		t.Fatal(err)
	}
	mutate := func(path, function string) {
		t.Helper()
		full := filepath.Join(captured, path)
		raw, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, path, raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || (function != "" && fn.Name.Name != function) {
				continue
			}
			offset := set.Position(fn.Body.Lbrace).Offset + 1
			changed := append([]byte{}, raw[:offset]...)
			changed = append(changed, []byte("\n_ = 12345\n")...)
			changed = append(changed, raw[offset:]...)
			if err := os.Chmod(full, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, changed, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(full, 0400); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatal("mutation function not found", path, function)
	}
	recapture := func() snapshotMetadata {
		t.Helper()
		m := impactMetadata(t, snapshot, captured)
		modes := map[string]uint32{}
		for _, entry := range before.Files {
			modes[entry.Path] = entry.Mode
		}
		for index := range m.Files {
			if mode, ok := modes[m.Files[index].Path]; ok {
				m.Files[index].Mode = mode
			}
		}
		if err := prepareRuntimeInputs(context.Background(), snapshot, map[string]string{"cli": captured}, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	mutate("tests/codexinstall/preflight.go", "reviewPreflight")
	after := recapture()
	if before.fingerprint("review-preflight") == after.fingerprint("review-preflight") {
		t.Fatal("real preflight edit reused its result")
	}
	for _, scenario := range []string{"install", "upgrade", "policy-recovery"} {
		if before.fingerprint(scenario) != after.fingerprint(scenario) {
			old := map[string]snapshotEntry{}
			for _, entry := range before.fingerprintFiles(scenario) {
				old[entry.Path] = entry
			}
			for _, entry := range after.fingerprintFiles(scenario) {
				if old[entry.Path] != entry {
					t.Log("changed input", entry.Path, old[entry.Path], entry)
				}
				delete(old, entry.Path)
			}
			for path := range old {
				t.Log("removed input", path)
			}
			t.Fatal("real preflight edit invalidated unrelated scenario", scenario)
		}
	}
	mutate("cmd/managed_bootstrap.go", "")
	product := recapture()
	for _, scenario := range scenarioNames {
		if after.fingerprint(scenario) == product.fingerprint(scenario) {
			t.Fatal("shared CLI edit reused scenario", scenario)
		}
	}
}
