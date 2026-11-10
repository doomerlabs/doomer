package e2eimpact

import (
	"strings"
	"testing"
)

const testPackage = "example.test/fixture"

func fixture() map[string][]byte {
	return map[string][]byte{
		"a.go":      []byte("package fixture; func A() int { return blargh() + shared() }"),
		"b.go":      []byte("package fixture; func B() int { return 7 + shared() }"),
		"blargh.go": []byte("package fixture; func blargh() int { return 1 }"),
		"shared.go": []byte("package fixture; func shared() int { return 2 }"),
	}
}

func analyze(t *testing.T, files map[string][]byte) Graph {
	t.Helper()
	g, err := AnalyzeGoPackage(testPackage, files)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func footprint(t *testing.T, g Graph, test string, extra ...string) Footprint {
	t.Helper()
	f, err := g.Trace(Entry{ID: "codex/" + test, Roots: []string{testPackage + "." + test}}, extra)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func key(t *testing.T, f Footprint) string {
	t.Helper()
	k, err := f.Fingerprint(map[string]string{"toolchain": "pinned", "model": "pinned", "platform": "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Exercise the cache decision itself, not just a list of changed filenames.
func TestMutationRerunsAAndReusesB(t *testing.T) {
	files := fixture()
	cache := map[string]bool{}
	executed := map[string]int{}
	run := func() {
		g := analyze(t, files)
		for _, test := range []string{"A", "B"} {
			f := footprint(t, g, test)
			if len(f.Fallback) != 0 {
				t.Fatalf("unexpected fallback: %v", f.Fallback)
			}
			fingerprint := key(t, f)
			if !cache[fingerprint] {
				executed[test]++
				cache[fingerprint] = true
			}
		}
	}
	run()
	files["blargh.go"] = []byte("package fixture; func blargh() int { return 99 }")
	run()
	if executed["A"] != 2 || executed["B"] != 1 {
		t.Fatalf("changing blargh.go should rerun A and reuse B: %v", executed)
	}
	files["shared.go"] = []byte("package fixture; func shared() int { return 42 }")
	run()
	if executed["A"] != 3 || executed["B"] != 2 {
		t.Fatalf("changing shared.go should rerun both: %v", executed)
	}
	files["b.go"] = []byte("package fixture; func B() int { return blargh() + shared() }")
	run()
	files["blargh.go"] = []byte("package fixture; func blargh() int { return 100 }")
	run()
	if executed["A"] != 4 || executed["B"] != 4 {
		t.Fatalf("new call edge should make blargh.go invalidate both: %v", executed)
	}
}

func TestTraceExplainsTransitiveDependency(t *testing.T) {
	f := footprint(t, analyze(t, fixture()), "A")
	for _, dep := range f.Dependencies {
		if dep.Path == "blargh.go" {
			if strings.Join(dep.Via, " -> ") != testPackage+".A -> "+testPackage+".blargh" {
				t.Fatal(dep.Via)
			}
			return
		}
	}
	t.Fatal("missing transitive dependency")
}

func TestPackageInitializersAffectEveryEntry(t *testing.T) {
	files := fixture()
	before := analyze(t, files)
	files["init.go"] = []byte("package fixture; var initialized = blargh(); func init() { _ = initialized }")
	after := analyze(t, files)
	for _, test := range []string{"A", "B"} {
		if key(t, footprint(t, before, test)) == key(t, footprint(t, after, test)) {
			t.Fatalf("new package initializer did not affect %s", test)
		}
	}
}

func TestMultipleSameLineInitializersCannotOverwriteDependencies(t *testing.T) {
	files := fixture()
	files["init.go"] = []byte("package fixture; var _ = blargh(); var _ = shared(); func init() { _ = blargh() }; func init() { _ = shared() }")
	g := analyze(t, files)
	if len(g.Always) != 4 {
		t.Fatalf("expected four distinct initializers, got %v", g.Always)
	}
	b := footprint(t, g, "B")
	found := false
	for _, dep := range b.Dependencies {
		found = found || dep.Path == "blargh.go"
	}
	if !found {
		t.Fatal("blank-variable/init dependency was lost")
	}
}

func TestCyclesTerminateAndStillIncludeEveryDependency(t *testing.T) {
	files := fixture()
	files["blargh.go"] = []byte("package fixture; func blargh() int { return A() }")
	f := footprint(t, analyze(t, files), "A")
	if len(f.Dependencies) != 3 {
		t.Fatalf("unexpected cyclic footprint: %+v", f)
	}
}

func TestDynamicCallsFallBackToAllInputs(t *testing.T) {
	for name, source := range map[string]string{
		"callback":           "package fixture; func A(callback func() int) int { return callback() }",
		"interface":          "package fixture; type value interface { Value() int }; func A(v value) int { return v.Value() }",
		"generic":            "package fixture; func A[T interface { Value() int }](v T) int { return v.Value() }",
		"embedded-interface": "package fixture; type value interface { Value() int }; type box struct { value }; func A(v box) int { return v.Value() }",
	} {
		t.Run(name, func(t *testing.T) {
			files := fixture()
			files["a.go"] = []byte(source)
			before := footprint(t, analyze(t, files), "A")
			if len(before.Fallback) == 0 || len(before.Dependencies) != len(files) {
				t.Fatalf("dynamic call must broaden dependencies: %+v", before)
			}
			files["b.go"] = []byte("package fixture; func B() int { return 1000 }")
			after := footprint(t, analyze(t, files), "A")
			if key(t, before) == key(t, after) {
				t.Fatal("fallback reused a changed input")
			}
		})
	}
}

func TestConcreteMethodsUseResolvedReceiver(t *testing.T) {
	files := map[string][]byte{
		"a.go":      []byte("package fixture; func A() int { return (First{}).Value() }"),
		"b.go":      []byte("package fixture; func B() int { return (Second{}).Value() }"),
		"first.go":  []byte("package fixture; type First struct{}; func (First) Value() int { return 1 }"),
		"second.go": []byte("package fixture; type Second struct{}; func (Second) Value() int { return 2 }"),
	}
	before := analyze(t, files)
	files["first.go"] = []byte("package fixture; type First struct{}; func (First) Value() int { return 9 }")
	after := analyze(t, files)
	if key(t, footprint(t, before, "A")) == key(t, footprint(t, after, "A")) {
		t.Fatal("receiver-specific method change did not invalidate A")
	}
	if key(t, footprint(t, before, "B")) != key(t, footprint(t, after, "B")) {
		t.Fatal("unrelated receiver method invalidated B")
	}
}

func TestBrokenAnalysisCannotBecomeAnEmptyCacheHit(t *testing.T) {
	files := fixture()
	delete(files, "blargh.go")
	if _, err := AnalyzeGoPackage(testPackage, files); err == nil {
		t.Fatal("deleted reachable function must fail analysis")
	}
	files = fixture()
	files["broken.go"] = []byte("not valid Go")
	if _, err := AnalyzeGoPackage(testPackage, files); err == nil {
		t.Fatal("invalid source must fail analysis")
	}
	if _, err := (Footprint{}).Fingerprint(nil); err == nil {
		t.Fatal("source-only identity accepted without frozen execution context")
	}
	if _, err := analyze(t, fixture()).Trace(Entry{ID: "codex/A", Roots: []string{testPackage + ".A"}}, []string{"uncaptured.json"}); err == nil {
		t.Fatal("extra input without captured bytes was accepted")
	}
}

func TestManifestAddsDependenciesWithoutRemovingAutomaticOnes(t *testing.T) {
	files := fixture()
	files["prompts/codex/review.txt"] = []byte("review carefully")
	files["prompts/codex/nested/task.txt"] = []byte("task")
	extras, err := ReadExtras(strings.NewReader("version: 1\nextra_dependencies:\n  'prompts/codex/**': ['codex/*']\n"))
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	expanded, err := extras.Expand(paths, []string{"codex/A", "codex/B", "claude/A"})
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded["codex/A"]) != 2 || len(expanded["codex/B"]) != 2 || len(expanded["claude/A"]) != 0 {
		t.Fatal(expanded)
	}
	f := footprint(t, analyze(t, files), "A", expanded["codex/A"]...)
	selected := map[string]bool{}
	for _, dep := range f.Dependencies {
		selected[dep.Path] = true
	}
	for _, want := range []string{"a.go", "blargh.go", "shared.go", "prompts/codex/review.txt", "prompts/codex/nested/task.txt"} {
		if !selected[want] {
			t.Fatal("missing automatically discovered or extra dependency", want)
		}
	}
	before := key(t, f)
	files["prompts/codex/review.txt"] = []byte("new instructions")
	if before == key(t, footprint(t, analyze(t, files), "A", expanded["codex/A"]...)) {
		t.Fatal("extra dependency edit was ignored")
	}
}

func TestManifestRejectsSilentTyposAndUnsafePaths(t *testing.T) {
	for _, source := range []string{
		"version: 2\nextra_dependencies: {}\n",
		"version: 1\nremove_dependencies: {}\n",
		"version: 1\nversion: 1\n",
		"version: 1\n---\nversion: 1\n",
	} {
		if _, err := ReadExtras(strings.NewReader(source)); err == nil {
			t.Fatal("invalid manifest accepted", source)
		}
	}
	for pattern, selectors := range map[string][]string{
		"missing.go": {"codex/*"}, "a.go": {"typo/*"}, "../a.go": {"codex/*"}, "[": {"codex/*"},
	} {
		e := Extras{Version: 1, ExtraDependencies: map[string][]string{pattern: selectors}}
		if _, err := e.Expand([]string{"a.go"}, []string{"codex/A"}); err == nil {
			t.Fatal("invalid rule accepted", pattern, selectors)
		}
	}
}

func TestAllTestsSelectorDeliberatelyCrossesHarnessSeparator(t *testing.T) {
	e := Extras{Version: 1, ExtraDependencies: map[string][]string{"a.go": {"*"}}}
	expanded, err := e.Expand([]string{"a.go"}, []string{"codex/A", "codex/B", "claude/A"})
	if err != nil {
		t.Fatal(err)
	}
	for id, paths := range expanded {
		if len(paths) != 1 || paths[0] != "a.go" {
			t.Fatalf("all-tests selector missed %s: %v", id, paths)
		}
	}
	e.ExtraDependencies["a.go"] = []string{"codex/["}
	if _, err := e.Expand([]string{"a.go"}, []string{"codex/A"}); err == nil {
		t.Fatal("malformed selector accepted")
	}
}

func TestFingerprintIgnoresExplanationsButIncludesExecutionSettings(t *testing.T) {
	f := footprint(t, analyze(t, fixture()), "A")
	before := key(t, f)
	f.Dependencies[0].Via = []string{"a different explanation"}
	if before != key(t, f) {
		t.Fatal("explanation text changed identity")
	}
	a, _ := f.Fingerprint(map[string]string{"model": "one"})
	b, _ := f.Fingerprint(map[string]string{"model": "two"})
	if a == b {
		t.Fatal("execution settings did not change identity")
	}
}
