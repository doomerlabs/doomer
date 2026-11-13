package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ScenarioFiles records a conservative dependency boundary, not line coverage.
// Imports, declarations and signatures remain shared. Only audited fixture
// function bodies can be omitted, and only while all their callers stay scoped.
type scenarioFile struct {
	CommonSHA256 string   `json:"common_sha256"`
	Scenarios    []string `json:"scenarios"`
}

func (m snapshotMetadata) fingerprintFiles(scenario string) []snapshotEntry {
	if len(m.RuntimeFiles) == 0 {
		return m.Files
	}
	selected := map[string]bool{}
	for _, path := range m.RuntimeFiles {
		selected[path] = true
	}
	known := false
	for _, name := range scenarioNames {
		known = known || name == scenario
	}
	explicit := map[string]bool{}
	for name, paths := range m.ExtraFiles {
		if name == scenario || !known {
			for _, path := range paths {
				selected[path] = true
				explicit[path] = true
			}
		}
	}
	var result []snapshotEntry
	for _, entry := range m.Files {
		if !selected[entry.Path] {
			continue
		}
		if explicit[entry.Path] {
			// Manual additions include the whole file, even if automatic
			// analysis considers some of its bodies unrelated.
			result = append(result, entry)
			continue
		}
		if scope, ok := m.ScenarioFiles[entry.Path]; ok && known && digestPattern.MatchString(scope.CommonSHA256) {
			relevant := false
			for _, name := range scope.Scenarios {
				relevant = relevant || name == scenario
			}
			if !relevant {
				entry.SHA256 = scope.CommonSHA256
			}
		}
		if hashes := m.ScenarioHashes[entry.Path]; known && digestPattern.MatchString(hashes[scenario]) {
			entry.SHA256 = hashes[scenario]
		}
		result = append(result, entry)
	}
	return result
}

// Resolve compiler inputs before pruning: a newly imported source file or
// embedded resource automatically becomes a dependency. Non-runtime files are
// then removed from the execution snapshot, so an undeclared filesystem read
// fails instead of silently depending on an unhashed document.
func prepareRuntimeInputs(ctx context.Context, snapshot string, roots map[string]string, m *snapshotMetadata) error {
	canonicalSnapshot, err := filepath.EvalSymlinks(snapshot)
	if err != nil {
		return err
	}
	snapshot = canonicalSnapshot
	canonicalRoots := map[string]string{}
	for role, root := range roots {
		canonicalRoots[role], err = filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
	}
	roots = canonicalRoots
	selected := map[string]bool{}
	add := func(root, path string) {
		absolute := path
		if !filepath.IsAbs(path) {
			absolute = filepath.Join(root, path)
		}
		relative, err := filepath.Rel(snapshot, absolute)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			selected[filepath.ToSlash(relative)] = true
		}
	}
	collect := func(root string, targets ...string) error {
		paths, err := compilerInputs(ctx, root, targets...)
		if err != nil {
			return err
		}
		for _, path := range paths {
			add(root, path)
		}
		return nil
	}
	if err := collect(roots["cli"], ".", "./tests/codexinstall"); err != nil {
		return err
	}
	if root := roots["candidate"]; root != "" && root != roots["cli"] {
		if err := collect(root, "."); err != nil {
			return err
		}
	}
	for _, path := range []string{
		"tests/codexinstall/onboarding-inputs.json", "tests/codexinstall/cache-keys.json",
		"tests/codexinstall/baseline-release.txt", "tests/codexinstall/Dockerfile",
		"scripts/ci-agent-e2e.sh", "scripts/agent-e2e-local-linux.sh",
		"scripts/agent-e2e-cache-plan.py",
		".depot/workflows/agent-e2e.yml",
		"e2e-extra-dependencies.yml",
	} {
		add(roots["cli"], path)
	}
	for _, entry := range m.Files {
		if strings.HasPrefix(entry.Path, "agent/") || strings.HasPrefix(entry.Path, "tooling/") {
			selected[entry.Path] = true
		}
		if root := roots["node"]; root != "" {
			relative, _ := filepath.Rel(snapshot, root)
			if strings.HasPrefix(entry.Path, filepath.ToSlash(relative)+"/") {
				selected[entry.Path] = true
			}
		}
		// Explicit installer/skill repositories retain their full inventory.
		if root := roots["skills"]; root != "" {
			relative, _ := filepath.Rel(snapshot, root)
			if strings.HasPrefix(entry.Path, filepath.ToSlash(relative)+"/") {
				selected[entry.Path] = true
			}
		}
	}
	// The manifest may only add inputs. Keep extra-only files in the execution
	// tree without turning an unrelated test's key into the entire file set.
	m.ExtraFiles, err = extraDependencyFiles(roots["cli"], snapshot, *m)
	if err != nil {
		return err
	}
	m.RuntimeFiles = nil
	for _, entry := range m.Files {
		if selected[entry.Path] {
			m.RuntimeFiles = append(m.RuntimeFiles, entry.Path)
		}
	}
	sort.Strings(m.RuntimeFiles)
	for _, paths := range m.ExtraFiles {
		for _, path := range paths {
			selected[path] = true
		}
	}
	m.DependencyAnalysis = analyzeDependencyShadow(ctx, roots["cli"])
	m.ScenarioHashes, err = scenarioBodyInputs(ctx, roots["cli"], snapshot)
	if err != nil {
		return err
	}
	m.ScenarioFiles = nil
	m.DependencyAnalysis.ScenarioProjection = "conservative"
	if len(m.ScenarioHashes) != 0 {
		m.DependencyAnalysis.ScenarioProjection = "enabled"
		m.DependencyAnalysis.ScenarioFiles = len(m.ScenarioHashes)
	}
	for _, entry := range m.Files {
		if !selected[entry.Path] {
			if err := os.Remove(filepath.Join(snapshot, filepath.FromSlash(entry.Path))); err != nil {
				return err
			}
		}
	}
	return nil
}

func compilerInputs(ctx context.Context, root string, targets ...string) ([]string, error) {
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "go", append([]string{"list", "-mod=readonly", "-deps", "-json"}, targets...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("resolve compiled inputs: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	selected := map[string]bool{}
	add := func(path string) {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			selected[path] = true
		}
	}
	for {
		var pkg struct {
			Dir                                                                                                                 string
			GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles []string
			Module                                                                                                              *struct{ GoMod string }
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		for _, files := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles} {
			for _, name := range files {
				add(filepath.Join(pkg.Dir, name))
			}
		}
		if pkg.Module != nil && pkg.Module.GoMod != "" {
			add(pkg.Module.GoMod)
			if path := filepath.Join(filepath.Dir(pkg.Module.GoMod), "go.sum"); fileExists(path) {
				add(path)
			}
		}
	}
	for _, name := range []string{"go.mod", "go.sum", "vendor/modules.txt"} {
		if path := filepath.Join(root, name); fileExists(path) {
			add(path)
		}
	}
	result := make([]string, 0, len(selected))
	for path := range selected {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func auditedDispatcher(file string, fn *ast.FuncDecl) bool {
	if ((file == "scenarios.go" && fn.Name.Name == "afterInstall") || (file == "onboarding.go" && fn.Name.Name == "onboarding")) && fn.Recv != nil && len(fn.Recv.List) == 1 {
		if fn.Type.Params.NumFields() != 0 {
			return false
		}
		field := fn.Recv.List[0]
		pointer, ok := field.Type.(*ast.StarExpr)
		if !ok || len(field.Names) != 1 || field.Names[0].Name != "j" {
			return false
		}
		kind, ok := pointer.X.(*ast.Ident)
		return ok && kind.Name == "journey"
	}
	if file == "worker.go" && fn.Name.Name == "runWorkerContext" && fn.Recv == nil {
		configParameters := 0
		for _, field := range fn.Type.Params.List {
			if kind, ok := field.Type.(*ast.Ident); ok && kind.Name == "config" {
				configParameters += len(field.Names)
			}
		}
		if configParameters != 1 {
			return false
		}
		for _, field := range fn.Type.Params.List {
			kind, ok := field.Type.(*ast.Ident)
			if ok && kind.Name == "config" {
				for _, name := range field.Names {
					if name.Name == "c" {
						return true
					}
				}
			}
		}
	}
	return false
}

type scopeVisitor struct {
	group          []string
	global         bool
	refine         bool
	scenarioSwitch bool
	check          func(*ast.Ident, []string, bool)
}

func (v scopeVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	if id, ok := node.(*ast.Ident); ok {
		v.check(id, v.group, v.global)
	}
	if statement, ok := node.(*ast.SwitchStmt); ok && v.refine && scenarioExpression(statement.Tag) {
		if statement.Init != nil {
			ast.Walk(v, statement.Init)
		}
		ast.Walk(v, statement.Tag)
		next := v
		next.scenarioSwitch = true
		ast.Walk(next, statement.Body)
		return nil
	}
	if clause, ok := node.(*ast.CaseClause); ok && v.scenarioSwitch {
		next := v
		next.group = nil
		next.scenarioSwitch = false
		for _, expression := range clause.List {
			literal, ok := expression.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				next.group = nil
				break
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				next.group = nil
				break
			}
			next.group = append(next.group, value)
		}
		for _, statement := range clause.Body {
			ast.Walk(next, statement)
		}
		return nil
	}
	if statement, ok := node.(*ast.IfStmt); ok {
		if statement.Init != nil {
			ast.Walk(v, statement.Init)
		}
		ast.Walk(v, statement.Cond)
		next := v
		if value, ok := scenarioCondition(statement.Cond); ok && v.refine {
			next.group = []string{value}
		}
		ast.Walk(next, statement.Body)
		if statement.Else != nil {
			ast.Walk(v, statement.Else)
		}
		return nil
	}
	return v
}

func journeyConfig(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "c" {
		return false
	}
	id, ok := selector.X.(*ast.Ident)
	return ok && id.Name == "j"
}
func scenarioExpression(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Scenario" {
		return false
	}
	if id, ok := selector.X.(*ast.Ident); ok {
		return id.Name == "c"
	}
	inner, ok := selector.X.(*ast.SelectorExpr)
	if !ok || inner.Sel.Name != "c" {
		return false
	}
	id, ok := inner.X.(*ast.Ident)
	return ok && id.Name == "j"
}
func scenarioCondition(expression ast.Expr) (string, bool) {
	binary, ok := expression.(*ast.BinaryExpr)
	if !ok || binary.Op != token.EQL {
		return "", false
	}
	left, right := binary.X, binary.Y
	if !scenarioExpression(left) {
		left, right = right, left
	}
	if !scenarioExpression(left) {
		return "", false
	}
	literal, ok := right.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}
