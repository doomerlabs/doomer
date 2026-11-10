// Package e2eimpact computes conservative source dependencies for a test.
// It does not authorize cache reuse or replace the runner's signed-input checks.
package e2eimpact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

type Node struct {
	File       string
	References []string
	Unresolved []string
}

type Graph struct {
	Nodes  map[string]Node
	Files  map[string][]byte
	Always []string
}

// Entry uses stable test names, not a test's position in a suite.
type Entry struct {
	ID    string
	Roots []string
}

type Dependency struct {
	Path   string   `json:"path"`
	SHA256 string   `json:"sha256"`
	Via    []string `json:"via"`
}

type Footprint struct {
	Test         string       `json:"test"`
	Dependencies []Dependency `json:"dependencies"`
	Fallback     []string     `json:"fallback,omitempty"`
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cleanPath(p string) bool {
	return p != "" && p != "." && !strings.ContainsAny(p, "\\:") && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}

// Trace retains a deterministic explanation for each file. An unresolved call
// broadens the footprint to every supplied source file rather than guessing.
// The caller must still capture non-source inputs (toolchain, external fixtures,
// model and execution settings) before using a footprint as a cache identity.
func (g Graph) Trace(entry Entry, extra []string) (Footprint, error) {
	out := Footprint{Test: entry.ID}
	if entry.ID == "" || len(entry.Roots) == 0 {
		return out, fmt.Errorf("test ID and entry points are required")
	}
	for p := range g.Files {
		if !cleanPath(p) {
			return out, fmt.Errorf("invalid source path %q", p)
		}
	}
	via := map[string][]string{}
	seen := map[string]bool{}
	var visit func(string, []string) error
	visit = func(id string, chain []string) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		node, ok := g.Nodes[id]
		if !ok {
			out.Fallback = append(out.Fallback, "unresolved symbol: "+id)
			return nil
		}
		chain = append(append([]string{}, chain...), id)
		if _, ok := g.Files[node.File]; !ok {
			return fmt.Errorf("symbol %s refers to missing file %s", id, node.File)
		}
		if _, exists := via[node.File]; !exists {
			via[node.File] = chain
		}
		for _, reason := range node.Unresolved {
			out.Fallback = append(out.Fallback, id+": "+reason)
		}
		refs := append([]string{}, node.References...)
		sort.Strings(refs)
		for _, ref := range refs {
			if err := visit(ref, chain); err != nil {
				return err
			}
		}
		return nil
	}
	roots := append(append([]string{}, entry.Roots...), g.Always...)
	sort.Strings(roots)
	for _, root := range roots {
		if err := visit(root, nil); err != nil {
			return out, err
		}
	}
	for _, p := range extra {
		if _, ok := g.Files[p]; !ok {
			return out, fmt.Errorf("extra dependency does not exist: %s", p)
		}
		if _, exists := via[p]; !exists {
			via[p] = []string{"e2e-extra-dependencies.yml", entry.ID}
		}
	}
	if len(out.Fallback) != 0 {
		for p := range g.Files {
			if _, exists := via[p]; !exists {
				via[p] = []string{"conservative fallback"}
			}
		}
	}
	sort.Strings(out.Fallback)
	out.Fallback = unique(out.Fallback)
	for p, chain := range via {
		out.Dependencies = append(out.Dependencies, Dependency{p, digest(g.Files[p]), chain})
	}
	sort.Slice(out.Dependencies, func(i, j int) bool { return out.Dependencies[i].Path < out.Dependencies[j].Path })
	return out, nil
}

// Fingerprint excludes explanation text, ordering and Git commit identity.
// The frozen context is mandatory: source hashes alone are not test results.
func (f Footprint) Fingerprint(context map[string]string) (string, error) {
	if len(context) == 0 {
		return "", fmt.Errorf("frozen execution context is required")
	}
	type input struct {
		Path, SHA256 string
	}
	files := make([]input, len(f.Dependencies))
	for i, dep := range f.Dependencies {
		files[i] = input{dep.Path, dep.SHA256}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	raw, err := json.Marshal(struct {
		Schema  int
		Test    string
		Files   []input
		Context map[string]string
	}{1, f.Test, files, context})
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}

func unique(values []string) []string {
	var out []string
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
