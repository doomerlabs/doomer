package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func impactFixture(t *testing.T, body, caller string) (string, snapshotMetadata) {
	t.Helper()
	snapshot := t.TempDir()
	root := filepath.Join(snapshot, "cli")
	files := map[string]string{
		"tests/codexinstall/upgrade.go":   "package main\nfunc (j *journey) upgrade() error { " + body + " }\n",
		"tests/codexinstall/scenarios.go": "package main\ntype journey struct { c config }; type config struct { Scenario string }; func (j *journey) afterInstall() error { switch j.c.Scenario { case \"upgrade\", \"upgrade-defer\", \"upgrade-untracked\", \"upgrade-personal\", \"upgrade-custom\", \"upgrade-authorized-session\": return j.upgrade() }; return nil }; " + caller,
	}
	for path, text := range files {
		writeImpactFile(t, root, path, text)
	}
	return root, impactMetadata(t, snapshot, root)
}
func writeImpactFile(t *testing.T, root, path, text string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func impactMetadata(t *testing.T, snapshot, root string) snapshotMetadata {
	t.Helper()
	m := testCacheMetadata()
	m.Schema = fingerprintSchema
	m.Files = nil
	m.RuntimeFiles = nil
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(snapshot, path)
		relative = filepath.ToSlash(relative)
		m.Files = append(m.Files, snapshotEntry{relative, byteHash(raw), 0600})
		m.RuntimeFiles = append(m.RuntimeFiles, relative)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	sort.Strings(m.RuntimeFiles)
	m.ScenarioFiles, err = scopedFixtureInputs(root, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestScenarioBodyChangesInvalidateOnlyItsDependents(t *testing.T) {
	root, before := impactFixture(t, "return nil", "")
	if _, ok := before.ScenarioFiles["cli/tests/codexinstall/upgrade.go"]; !ok {
		t.Fatal("upgrade fixture unexpectedly shared")
	}
	writeImpactFile(t, root, "tests/codexinstall/upgrade.go", "package main\nfunc (j *journey) upgrade() error {\n\n// a different-length body\nif j.c.Scenario == \"upgrade\" { return nil }; return nil\n}\n")
	after := impactMetadata(t, filepath.Dir(root), root)
	if before.fingerprint("install") != after.fingerprint("install") {
		t.Fatal("unrelated install invalidated")
	}
	for _, scenario := range fixtureScopes["upgrade.go"] {
		if before.fingerprint(scenario) == after.fingerprint(scenario) {
			t.Fatal("changed upgrade dependency reused", scenario)
		}
	}
	if before.fingerprint("unknown-future-scenario") == after.fingerprint("unknown-future-scenario") {
		t.Fatal("unknown scenario was not conservative")
	}
}
func TestScenarioHeadersAndUnexpectedCallersStayShared(t *testing.T) {
	for name, source := range map[string]string{
		"imports":        "package main\nimport \"fmt\"\nfunc (j *journey) upgrade() error { return fmt.Errorf(\"changed\") }",
		"global":         "package main\nvar shared = 1\nfunc (j *journey) upgrade() error { return nil }",
		"signature":      "package main\nfunc (j *journey) upgrade(extra bool) error { return nil }",
		"initialization": "package main\nfunc init() { panic(\"changed\") }\nfunc (j *journey) upgrade() error { return nil }",
	} {
		t.Run(name, func(t *testing.T) {
			root, before := impactFixture(t, "return nil", "")
			writeImpactFile(t, root, "tests/codexinstall/upgrade.go", source)
			after := impactMetadata(t, filepath.Dir(root), root)
			if before.fingerprint("install") == after.fingerprint("install") {
				t.Fatal("shared declaration change was ignored")
			}
		})
	}
	for name, caller := range map[string]string{
		"unconditional":       "func shared(j *journey) error { return j.upgrade() }",
		"different-scenario":  "func shared(j *journey) error { if j.c.Scenario == \"install\" { return j.upgrade() }; return nil }",
		"global-reference":    "var callback = (*journey).upgrade",
		"reflection":          "func invoke(j *journey) { value.MethodByName(\"upgrade\") }",
		"unaudited-condition": "func shared(j *journey) error { if j.c.Scenario == \"upgrade\" { return j.upgrade() }; return nil }",
		"mutated-scenario":    "func mutate(j *journey) { j.c.Scenario = \"upgrade\" }",
		"aliased-config":      "func mutate(j *journey) { p := &j.c; p.Scenario = \"upgrade\" }",
		"substituted-config":  "func mutate(j *journey) { _ = config{Scenario: \"upgrade\"} }",
	} {
		t.Run(name, func(t *testing.T) {
			root, before := impactFixture(t, "return nil", caller)
			if _, ok := before.ScenarioFiles["cli/tests/codexinstall/upgrade.go"]; ok {
				t.Fatal("unexpected caller did not make fixture shared")
			}
			writeImpactFile(t, root, "tests/codexinstall/upgrade.go", "package main\nfunc (j *journey) upgrade() error { panic(\"changed\") }")
			after := impactMetadata(t, filepath.Dir(root), root)
			if before.fingerprint("install") == after.fingerprint("install") {
				t.Fatal("shared fixture body reused")
			}
		})
	}
}
func TestActualScenarioScopeAudit(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := scopedFixtureInputs(root, filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	audit, err := fixtureAuditDigest(root, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if audit != fixtureAuditSHA256 {
		t.Fatalf("shared runner changed; review fixture scopes before updating audit digest: %s", audit)
	}
	for _, name := range []string{"upgrade.go", "recovery.go", "portable.go", "preflight.go", "command_permissions.go", "policy_recovery.go", "hook_setup.go", "unconfigured_repo.go", "browser_onboarding.go"} {
		path := filepath.Base(root) + "/tests/codexinstall/" + name
		if _, ok := scopes[path]; !ok {
			t.Errorf("%s is not scoped; audit its callers", name)
		}
	}
}
func TestCompilerInputsIgnoreDocsButRetainEmbedsAndNewSource(t *testing.T) {
	makeFixture := func(document, asset, extra string) snapshotMetadata {
		snapshot := t.TempDir()
		root := filepath.Join(snapshot, "cli")
		files := map[string]string{"go.mod": "module example.test/impact\n\ngo 1.23\n", "main.go": "package main\nimport _ \"embed\"\n//go:embed assets/value.txt\nvar value string\nfunc main() {}\n", "assets/value.txt": asset, "AGENTS.md": document, "docs/readme.md": document, "tests/codexinstall/main.go": "package main\nfunc main() {}\n"}
		if extra != "" {
			files["added.go"] = extra
		}
		for path, text := range files {
			writeImpactFile(t, root, path, text)
		}
		m := impactMetadata(t, snapshot, root)
		if err := prepareRuntimeInputs(context.Background(), snapshot, map[string]string{"cli": root}, &m); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"AGENTS.md", "docs/readme.md"} {
			if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
				t.Fatal("unhashed document still readable by execution", path, err)
			}
		}
		if raw, err := os.ReadFile(filepath.Join(root, "assets/value.txt")); err != nil || string(raw) != asset {
			t.Fatal("embedded file was pruned", err)
		}
		return m
	}
	a := makeFixture("docs one", "asset one", "")
	b := makeFixture("docs two", "asset one", "")
	if a.fingerprint("install") != b.fingerprint("install") {
		t.Fatal("documentation changed runtime identity")
	}
	c := makeFixture("docs two", "asset two", "")
	if b.fingerprint("install") == c.fingerprint("install") {
		t.Fatal("embedded resource change was ignored")
	}
	d := makeFixture("docs two", "asset one", "package main\nvar added = 1\n")
	if b.fingerprint("install") == d.fingerprint("install") {
		t.Fatal("new compiled source was ignored")
	}
}
func TestSignedResultReusedAcrossUnrelatedFixtureChange(t *testing.T) {
	root, before := impactFixture(t, "return nil", "")
	before.Commit = "original-execution"
	keys := testCacheKey(t)
	store := &memoryCache{objects: map[string][]byte{}}
	path := filepath.Join(t.TempDir(), "report.json")
	writeCacheReport(t, path, true)
	if err := publishCachedResult(context.Background(), store, before, "install", path, time.Now()); err != nil {
		t.Fatal(err)
	}
	writeImpactFile(t, root, "tests/codexinstall/upgrade.go", "package main\nfunc (j *journey) upgrade() error { panic(\"changed\") }")
	after := impactMetadata(t, filepath.Dir(root), root)
	after.Commit = "current-run"
	target := filepath.Join(t.TempDir(), "reused.json")
	if err := reuseCachedResult(context.Background(), store, after, "install", target, keys); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var r report
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if r.Cache == nil || !r.Cache.Hit {
		t.Fatal("result was not marked cached")
	}
	if r.Cache.ExecutedCommit != before.Commit || r.Cache.ExecutedAt.IsZero() {
		t.Fatal("original execution provenance was lost")
	}
	if !strings.Contains(string(raw), "fingerprint") {
		t.Fatal("missing provenance")
	}
}

func TestPrunedRuntimeBuildsRealCLIAndRunner(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := t.TempDir()
	entries, err := copySource(context.Background(), root, snapshot, "cli", "")
	if err != nil {
		t.Fatal(err)
	}
	m := testCacheMetadata()
	m.Files = entries
	captured := filepath.Join(snapshot, "cli")
	if err = prepareRuntimeInputs(context.Background(), snapshot, map[string]string{"cli": captured}, &m); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{".", "./tests/codexinstall"} {
		binary := filepath.Join(t.TempDir(), "program.exe")
		cmd := exec.Command("go", "build", "-mod=readonly", "-buildvcs=false", "-o", binary, target)
		cmd.Dir = captured
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("pruned %s build: %v: %s", target, err, output)
		}
	}
}

func TestScopedBodiesIgnoreOffsetsButKeepCompilerDirectives(t *testing.T) {
	root, _ := impactFixture(t, "return nil", "")
	path := "tests/codexinstall/upgrade.go"
	writeImpactFile(t, root, path, "package main\nfunc (j *journey) upgrade() error { return nil }\nfunc helper() {}\n")
	before := impactMetadata(t, filepath.Dir(root), root)
	writeImpactFile(t, root, path, "package main\nfunc (j *journey) upgrade() error {\n\n\nreturn nil\n}\nfunc helper() {\n}\n")
	after := impactMetadata(t, filepath.Dir(root), root)
	if before.fingerprint("install") != after.fingerprint("install") {
		t.Fatal("body line offsets changed shared identity")
	}
	writeImpactFile(t, root, path, "//go:debug panicnil=1\npackage main\nfunc (j *journey) upgrade() error { return nil }\nfunc helper() {}\n")
	directive := impactMetadata(t, filepath.Dir(root), root)
	if before.fingerprint("install") == directive.fingerprint("install") {
		t.Fatal("compiler directive was ignored")
	}
}

func TestScopeAuditSurvivesBodyEditButNotSharedControlChange(t *testing.T) {
	root, before := impactFixture(t, "return nil", "")
	audited, err := fixtureAuditDigest(root, before.ScenarioFiles)
	if err != nil {
		t.Fatal(err)
	}
	writeImpactFile(t, root, "tests/codexinstall/upgrade.go", "package main\nfunc (j *journey) upgrade() error { panic(\"changed body\") }")
	body := impactMetadata(t, filepath.Dir(root), root)
	digest, err := fixtureAuditDigest(root, body.ScenarioFiles)
	if err != nil {
		t.Fatal(err)
	}
	if digest != audited {
		t.Fatal("body-only change broke the scope audit")
	}
	shared := filepath.Join(root, "tests/codexinstall/scenarios.go")
	raw, err := os.ReadFile(shared)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(shared, append(raw, []byte("\nfunc sharedControl() { panic(\"new shared control\") }\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	changed := impactMetadata(t, filepath.Dir(root), root)
	digest, err = fixtureAuditDigest(root, changed.ScenarioFiles)
	if err != nil {
		t.Fatal(err)
	}
	if digest == audited {
		t.Fatal("shared control change retained the old scope audit")
	}
}

func TestUpdatingAuditPinCannotReuseSharedRunnerResults(t *testing.T) {
	root, _ := impactFixture(t, "return nil", "")
	writeImpactFile(t, root, "tests/codexinstall/impact.go", "package main\nconst fixtureAuditSHA256 = \"old audit\"\n")
	before := impactMetadata(t, filepath.Dir(root), root)
	shared := filepath.Join(root, "tests/codexinstall/scenarios.go")
	raw, err := os.ReadFile(shared)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, append(raw, []byte("\nfunc sharedControl() { panic(\"changed\") }\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	changed := impactMetadata(t, filepath.Dir(root), root)
	digest, err := fixtureAuditDigest(root, changed.ScenarioFiles)
	if err != nil {
		t.Fatal(err)
	}
	writeImpactFile(t, root, "tests/codexinstall/impact.go", "package main\nconst fixtureAuditSHA256 = \""+digest+"\"\n")
	after := impactMetadata(t, filepath.Dir(root), root)
	if len(after.ScenarioFiles) == 0 {
		t.Fatal("test must retain narrow scopes after updating the audit pin")
	}
	for _, scenario := range scenarioNames {
		if before.fingerprint(scenario) == after.fingerprint(scenario) {
			t.Fatalf("shared runner and audit-pin change reused %s", scenario)
		}
	}
}
