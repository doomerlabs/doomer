package e2eimpact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/importer"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// LoadGoGraph analyzes the compiler-selected packages in a captured tree.
// Callers must use the same Go environment for this analysis and execution.
// Export data supplies types for external dependencies, but external behavior
// remains unresolved: this function never assumes such calls are pure.
func LoadGoGraph(ctx context.Context, root string, targets ...string) (Graph, []string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Graph{}, nil, err
	}
	cmd := exec.CommandContext(ctx, "go", append([]string{"list", "-mod=readonly", "-deps", "-export", "-json"}, targets...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	raw, err := cmd.Output()
	if err != nil {
		return Graph{}, nil, fmt.Errorf("resolve dependency graph: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var packages []Package
	var roots []string
	exports := map[string]string{}
	for {
		var pkg struct {
			ImportPath, Dir, Export, Name                    string
			DepOnly                                          bool
			GoFiles, CgoFiles, EmbedFiles, SFiles, SysoFiles []string
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return Graph{}, nil, err
		}
		exports[pkg.ImportPath] = pkg.Export
		if !pkg.DepOnly && pkg.Name != "main" {
			return Graph{}, nil, fmt.Errorf("dependency roots must be command packages: %s", pkg.ImportPath)
		}
		relative, err := filepath.Rel(root, pkg.Dir)
		if err != nil || !inside(relative) {
			continue
		}
		if len(pkg.CgoFiles)+len(pkg.SFiles)+len(pkg.SysoFiles) != 0 {
			return Graph{}, nil, fmt.Errorf("native code requires conservative analysis: %s", pkg.ImportPath)
		}
		p := Package{ImportPath: pkg.ImportPath, Sources: map[string][]byte{}}
		for _, name := range append(append([]string{}, pkg.GoFiles...), pkg.EmbedFiles...) {
			full := filepath.Join(pkg.Dir, name)
			resolved, err := filepath.EvalSymlinks(full)
			if err != nil {
				return Graph{}, nil, err
			}
			rel, err := filepath.Rel(root, resolved)
			if err != nil || !inside(rel) {
				return Graph{}, nil, fmt.Errorf("dependency escapes snapshot: %s", full)
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return Graph{}, nil, err
			}
			p.Sources[filepath.ToSlash(rel)] = data
		}
		packages = append(packages, p)
		if !pkg.DepOnly {
			roots = append(roots, pkg.ImportPath+".main")
		}
	}
	external := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		file := exports[path]
		if file == "" {
			return nil, fmt.Errorf("missing compiler export for %s", path)
		}
		return os.Open(file)
	})
	g, err := analyzeGoPackages(packages, external)
	if err != nil {
		return Graph{}, nil, err
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Graph{}, nil, err
		}
		g.Files[name] = data
		id := "file:" + name
		g.Nodes[id] = Node{File: name}
		g.Always = append(g.Always, id)
	}
	sort.Strings(roots)
	return g, roots, nil
}

func inside(relative string) bool {
	return relative == "." || cleanPath(filepath.ToSlash(relative))
}
