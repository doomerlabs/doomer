package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/e2eimpact"
)

// Shadow results explain the graph but never narrow the signed cache key.
// Agent-selected CLI commands still require the conservative source boundary.
type dependencyAnalysis struct {
	Mode      string               `json:"mode"`
	Files     int                  `json:"files,omitempty"`
	Fallbacks int                  `json:"fallbacks,omitempty"`
	Examples  []string             `json:"examples,omitempty"`
	Footprint *e2eimpact.Footprint `json:"-"`
	Error     string               `json:"error,omitempty"`
}

func analyzeDependencyShadow(ctx context.Context, root string) *dependencyAnalysis {
	result := &dependencyAnalysis{Mode: "shadow"}
	graph, roots, err := e2eimpact.LoadGoGraph(ctx, root, ".", "./tests/codexinstall")
	if err == nil {
		var footprint e2eimpact.Footprint
		footprint, err = graph.Trace(e2eimpact.Entry{ID: "shared-agent-runtime", Roots: roots}, nil)
		if err == nil {
			result.Footprint = &footprint
			result.Files = len(footprint.Dependencies)
			result.Fallbacks = len(footprint.Fallback)
			result.Examples = footprint.Fallback[:min(5, len(footprint.Fallback))]
		}
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func extraDependencyFiles(root, snapshot string, metadata snapshotMetadata) (map[string][]string, error) {
	file, err := os.Open(filepath.Join(root, "e2e-extra-dependencies.yml"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	extras, err := e2eimpact.ReadExtras(file)
	if err != nil {
		return nil, fmt.Errorf("extra E2E dependencies: %w", err)
	}
	prefix, err := filepath.Rel(snapshot, root)
	if err != nil {
		return nil, err
	}
	prefix = filepath.ToSlash(prefix) + "/"
	var files, tests []string
	for _, entry := range metadata.Files {
		if strings.HasPrefix(entry.Path, prefix) {
			files = append(files, strings.TrimPrefix(entry.Path, prefix))
		}
	}
	scenarios := append(append([]string{}, scenarioNames...), "ordinary-upgrade", "policy-activation")
	for _, harness := range []string{"codex", "claude"} {
		for _, scenario := range scenarios {
			tests = append(tests, harness+"/"+scenario)
		}
	}
	expanded, err := extras.Expand(files, tests)
	if err != nil {
		return nil, fmt.Errorf("extra E2E dependencies: %w", err)
	}
	result := map[string][]string{}
	harness := metadata.Settings["harness"]
	if harness != "codex" && harness != "claude" {
		return nil, fmt.Errorf("unknown E2E harness %q", harness)
	}
	for _, scenario := range scenarios {
		for _, path := range expanded[harness+"/"+scenario] {
			result[scenario] = append(result[scenario], prefix+path)
		}
	}
	return result, nil
}
