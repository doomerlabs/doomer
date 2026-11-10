package e2eimpact

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"strings"
)

// Package contains only compiler-selected files from one captured package.
// File names must be relative to the same snapshot root across all packages.
type Package struct {
	ImportPath string
	Sources    map[string][]byte
}

// AnalyzeGoPackages resolves calls between captured packages. Imports outside
// this set still cause conservative fallback, including their initialization.
func AnalyzeGoPackages(packages []Package) (Graph, error) {
	return analyzeGoPackages(packages, importer.Default())
}

func analyzeGoPackages(packages []Package, external types.Importer) (Graph, error) {
	if len(packages) == 0 {
		return Graph{}, fmt.Errorf("no captured Go packages")
	}
	imports := &sourceImporter{sources: map[string]map[string][]byte{}, packages: map[string]*types.Package{}, loading: map[string]bool{}, fallback: external}
	known := map[string]bool{}
	for _, pkg := range packages {
		if pkg.ImportPath == "" || known[pkg.ImportPath] {
			return Graph{}, fmt.Errorf("empty or duplicate package: %s", pkg.ImportPath)
		}
		known[pkg.ImportPath] = true
		imports.sources[pkg.ImportPath] = pkg.Sources
	}
	g := Graph{Nodes: map[string]Node{}, Files: map[string][]byte{}}
	paths := make([]string, 0, len(known))
	for path := range known {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		part, err := analyzeGoPackage(path, imports.sources[path], imports, known)
		if err != nil {
			return Graph{}, fmt.Errorf("analyze %s: %w", path, err)
		}
		for file, data := range part.Files {
			if _, exists := g.Files[file]; exists {
				return Graph{}, fmt.Errorf("duplicate captured file: %s", file)
			}
			g.Files[file] = data
		}
		for id, node := range part.Nodes {
			g.Nodes[id] = node
		}
		g.Always = append(g.Always, part.Always...)
	}
	sort.Strings(g.Always)
	return g, nil
}

type sourceImporter struct {
	sources  map[string]map[string][]byte
	packages map[string]*types.Package
	loading  map[string]bool
	fallback types.Importer
}

func (s *sourceImporter) Import(path string) (*types.Package, error) {
	if pkg := s.packages[path]; pkg != nil {
		return pkg, nil
	}
	sources, ok := s.sources[path]
	if !ok {
		return s.fallback.Import(path)
	}
	if s.loading[path] {
		return nil, fmt.Errorf("import cycle: %s", path)
	}
	s.loading[path] = true
	defer delete(s.loading, path)
	set := token.NewFileSet()
	var files []*ast.File
	var names []string
	for name := range sources {
		if strings.HasSuffix(name, ".go") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		file, err := parser.ParseFile(set, name, sources[name], parser.AllErrors)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	config := types.Config{Importer: s}
	pkg, err := config.Check(path, set, files, nil)
	if err == nil {
		s.packages[path] = pkg
	}
	return pkg, err
}
