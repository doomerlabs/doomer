package e2eimpact

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCrossPackageMutation(t *testing.T) {
	packages := []Package{
		{ImportPath: testPackage, Sources: map[string][]byte{
			"a.go": []byte("package fixture; import \"example.test/lib\"; func A() int { return lib.Value() }"),
			"b.go": []byte("package fixture; func B() int { return 2 }"),
		}},
		{ImportPath: "example.test/lib", Sources: map[string][]byte{"lib/blargh.go": []byte("package lib; func Value() int { return 1 }")}},
	}
	trace := func(id string) string {
		t.Helper()
		g, err := AnalyzeGoPackages(packages)
		if err != nil {
			t.Fatal(err)
		}
		f := footprint(t, g, id)
		if len(f.Fallback) != 0 {
			t.Fatal(f.Fallback)
		}
		return key(t, f)
	}
	a, b := trace("A"), trace("B")
	packages[1].Sources["lib/blargh.go"] = []byte("package lib; func Value() int { return 99 }")
	if a == trace("A") || b != trace("B") {
		t.Fatal("cross-package edit must rerun A and reuse B")
	}
	packages[1].Sources["lib/init.go"] = []byte("package lib; var initialized = Value()")
	b = trace("B")
	packages[1].Sources["lib/blargh.go"] = []byte("package lib; func Value() int { return 101 }")
	if b == trace("B") {
		t.Fatal("imported package initializer must invalidate B too")
	}
}

func TestCrossPackageBrokenSourceAndImportCycleFail(t *testing.T) {
	for _, packages := range [][]Package{
		{{ImportPath: "example.test/a", Sources: map[string][]byte{"a.go": []byte("package a; import \"example.test/b\"; func A() { b.Missing() }")}}, {ImportPath: "example.test/b", Sources: map[string][]byte{"b.go": []byte("package b")}}},
		{{ImportPath: "example.test/a", Sources: map[string][]byte{"a.go": []byte("package a; import _ \"example.test/b\"")}}, {ImportPath: "example.test/b", Sources: map[string][]byte{"b.go": []byte("package b; import _ \"example.test/a\"")}}},
	} {
		if _, err := AnalyzeGoPackages(packages); err == nil {
			t.Fatal("invalid graph accepted")
		}
	}
}

func TestLoaderUsesCompilerSelection(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":            "module example.test/load\n\ngo 1.23\n",
		"main.go":           "package main; import \"example.test/load/lib\"; func main() { _ = lib.Value() }",
		"lib/value.go":      "package lib; func Value() int { return 3 }",
		"lib/ignored.go":    "//go:build never_selected\n\npackage lib; invalid source ignored by compiler",
		"lib/value_test.go": "package lib; invalid test source excluded from executable",
	} {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	g, roots, err := LoadGoGraph(context.Background(), root, ".")
	if err != nil {
		t.Fatal(err)
	}
	f, err := g.Trace(Entry{ID: "codex/example", Roots: roots}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Fallback) != 0 {
		t.Fatal(f.Fallback)
	}
	if len(g.Files) != 3 {
		t.Fatalf("unexpected compiler inputs: %v", g.Files)
	}
	before := key(t, f)
	if err := os.WriteFile(filepath.Join(root, "lib/value.go"), []byte("package lib; func Value() int { return 4 }"), 0600); err != nil {
		t.Fatal(err)
	}
	g, roots, err = LoadGoGraph(context.Background(), root, ".")
	if err != nil {
		t.Fatal(err)
	}
	f, err = g.Trace(Entry{ID: "codex/example", Roots: roots}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key(t, f) == before {
		t.Fatal("loader missed transitive source change")
	}
	if _, _, err := LoadGoGraph(context.Background(), root, "./lib"); err == nil {
		t.Fatal("library target returned a nonexistent main entry point")
	}
}
