package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
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

var fixtureScopes = map[string][]string{
	"browser_onboarding.go":  {"browser-onboarding"},
	"unconfigured_repo.go":   {"unconfigured-repo"},
	"upgrade.go":             {"upgrade", "upgrade-defer", "upgrade-untracked", "upgrade-personal", "upgrade-custom", "upgrade-authorized-session", "upgrade-personal-npx", "upgrade-repo-over-personal-npx"},
	"npx_upgrade.go":         {"upgrade-personal-npx", "upgrade-repo-over-personal-npx"},
	"recovery.go":            {"portable-invalid", "revoked-uploads", "interrupted-review"},
	"portable.go":            {"portable-restart"},
	"preflight.go":           {"review-preflight"},
	"command_permissions.go": {"review-command-permissions", "policy-installed"},
	"policy_recovery.go":     {"policy-recovery", "policy-declined"},
	"hook_setup.go":          {"hooks-upgrade", "hooks-declined", "hooks-trust"},
}

// Updating shared runner control flow requires reviewing the scope boundaries.
// A mismatch safely falls back to shared fixture bodies until that audit occurs.
// Personal/repository discovery remains shared; its new dispatch paths do not
// call the existing scenario-scoped fixture helpers.
const fixtureAuditSHA256 = "8c532f4138d0275ec35c849d55c5681c3d84bd0a08c666a7d8dff71bbe55257d"

func (m snapshotMetadata) fingerprintFiles(scenario string) []snapshotEntry {
	if len(m.RuntimeFiles) == 0 {
		return m.Files
	}
	selected := map[string]bool{}
	for _, path := range m.RuntimeFiles {
		selected[path] = true
	}
	known := scenario == "ordinary-upgrade" || scenario == "policy-activation"
	for _, name := range scenarioNames {
		known = known || name == scenario
	}
	var result []snapshotEntry
	for _, entry := range m.Files {
		if !selected[entry.Path] {
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
		"internal/managed/bootstrap-2026.11.6.10.md", "tests/codexinstall/cache-keys.json",
		"tests/codexinstall/baseline-release.txt", "tests/codexinstall/Dockerfile",
		"scripts/ci-agent-e2e.sh", "scripts/agent-e2e-local-linux.sh",
		"scripts/agent-e2e-baseline.sh", "scripts/agent-e2e-cache-plan.py",
		".depot/workflows/agent-e2e.yml",
	} {
		add(roots["cli"], path)
	}
	if root := roots["bootstrap"]; root != "" {
		for _, path := range []string{".agents/skills/doomer-review/SKILL.md", ".claude/skills/doomer-review/SKILL.md"} {
			add(root, path)
		}
	}
	for _, entry := range m.Files {
		if strings.HasPrefix(entry.Path, "agent/") {
			selected[entry.Path] = true
		}
		// Explicit installer/skill repositories retain their full inventory.
		if root := roots["skills"]; root != "" {
			relative, _ := filepath.Rel(snapshot, root)
			if strings.HasPrefix(entry.Path, filepath.ToSlash(relative)+"/") {
				selected[entry.Path] = true
			}
		}
	}
	m.RuntimeFiles = nil
	for _, entry := range m.Files {
		if selected[entry.Path] {
			m.RuntimeFiles = append(m.RuntimeFiles, entry.Path)
		}
	}
	sort.Strings(m.RuntimeFiles)
	m.ScenarioFiles, err = scopedFixtureInputs(roots["cli"], snapshot)
	if err != nil {
		return err
	}
	if audit, err := fixtureAuditDigest(roots["cli"], m.ScenarioFiles); err != nil {
		return err
	} else if audit != fixtureAuditSHA256 {
		m.ScenarioFiles = nil
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

// Any unexpected caller promotes a fixture back to shared input. This makes
// future cross-scenario helpers conservative without requiring a map update.
func scopedFixtureInputs(root, snapshot string) (map[string]scenarioFile, error) {
	directory := filepath.Join(root, "tests/codexinstall")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	files := map[string]*ast.File{}
	scopes := map[string][]string{}
	definitions := map[string][]string{}
	definitionPositions := map[token.Pos]bool{}
	set := token.NewFileSet()
	opaque := false
	for _, entry := range entries {
		name := entry.Name()
		switch strings.ToLower(filepath.Ext(name)) {
		case ".s", ".c", ".cc", ".cpp", ".m", ".f", ".f90":
			opaque = true
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return nil, err
		}
		parsed, err := parser.ParseFile(set, name, raw, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files[name] = parsed
		if group, ok := fixtureScopes[name]; ok {
			scopes[name] = append([]string(nil), group...)
		}
		for _, group := range parsed.Comments {
			for _, comment := range group.List {
				if strings.HasPrefix(comment.Text, "//go:linkname") {
					opaque = true
				}
			}
		}
		reflectNames := map[string]bool{}
		for _, imp := range parsed.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path == "unsafe" || path == "C" || path == "plugin" {
				opaque = true
			}
			if path == "reflect" {
				alias := "reflect"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				if alias == "." {
					opaque = true
				}
				reflectNames[alias] = true
			}
		}
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				definitionPositions[fn.Name.Pos()] = true
				if fn.Name.Name == "init" {
					delete(scopes, name)
				}
				if _, ok := fixtureScopes[name]; ok {
					definitions[fn.Name.Name] = append(definitions[fn.Name.Name], name)
				}
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				if pkg, ok := selector.X.(*ast.Ident); ok && reflectNames[pkg.Name] && selector.Sel.Name != "DeepEqual" {
					opaque = true
				}
			}
			// Scenario identity must remain immutable after dispatch. Mutation or
			// aliases would make a syntactic branch insufficient evidence.
			if assignment, ok := node.(*ast.AssignStmt); ok {
				for _, left := range assignment.Lhs {
					if scenarioExpression(left) || journeyConfig(left) {
						opaque = true
					}
				}
			}
			if address, ok := node.(*ast.UnaryExpr); ok && address.Op == token.AND && (scenarioExpression(address.X) || journeyConfig(address.X)) {
				opaque = true
			}
			if literal, ok := node.(*ast.CompositeLit); ok {
				if kind, ok := literal.Type.(*ast.Ident); ok && kind.Name == "config" {
					for _, item := range literal.Elts {
						pair, ok := item.(*ast.KeyValueExpr)
						if !ok {
							opaque = true
							continue
						}
						key, ok := pair.Key.(*ast.Ident)
						if !ok || key.Name != "Scenario" {
							continue
						}
						flag, ok := pair.Value.(*ast.StarExpr)
						if name != "main.go" || !ok {
							opaque = true
							continue
						}
						id, ok := flag.X.(*ast.Ident)
						if !ok || id.Name != "scenario" {
							opaque = true
						}
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				switch selector.Sel.Name {
				case "MethodByName", "Method", "Call", "CallSlice", "UnsafeAddr", "NewAt":
					opaque = true
				}
			}
			return true
		})
	}
	if opaque {
		return map[string]scenarioFile{}, nil
	}
	for changed := true; changed; {
		changed = false
		for name, file := range files {
			check := func(id *ast.Ident, group []string, global bool) {
				if definitionPositions[id.Pos()] {
					return
				}
				for _, target := range definitions[id.Name] {
					targetScope, ok := scopes[target]
					if !ok {
						continue
					}
					if target == name && !global {
						continue
					}
					if !scopeSubset(group, targetScope) {
						delete(scopes, target)
						changed = true
					}
				}
			}
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					group := scopes[name]
					if fn.Name.Name == "init" {
						group = nil
					}
					refine := auditedDispatcher(name, fn)
					if refine {
						identity := "j"
						if name == "worker.go" {
							identity = "c"
						}
						ast.Inspect(fn.Body, func(node ast.Node) bool {
							if assignment, ok := node.(*ast.AssignStmt); ok {
								for _, left := range assignment.Lhs {
									if id, ok := left.(*ast.Ident); ok && id.Name == identity {
										refine = false
									}
								}
							}
							return true
						})
					}
					ast.Walk(scopeVisitor{group: group, refine: refine, check: check}, fn)
				} else {
					ast.Walk(scopeVisitor{global: true, check: check}, decl)
				}
			}
		}
	}
	result := map[string]scenarioFile{}
	for name, group := range scopes {
		common, err := canonicalGoDigest(filepath.Join(directory, name), true)
		if err != nil {
			return nil, err
		}
		relative, _ := filepath.Rel(snapshot, filepath.Join(directory, name))
		sort.Strings(group)
		result[filepath.ToSlash(relative)] = scenarioFile{common, group}
	}
	return result, nil
}

func canonicalGoDigest(path string, omitBodies bool) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, raw, parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	if omitBodies {
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				fn.Body = nil
			}
		}
	}
	var canonical bytes.Buffer
	// A fresh FileSet deliberately omits source line positions from identity.
	if err := format.Node(&canonical, token.NewFileSet(), parsed); err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//go:") || strings.HasPrefix(line, "// +build") || strings.HasPrefix(line, "//line ") {
			canonical.WriteString("\n" + line)
		}
	}
	return byteHash(canonical.Bytes()), nil
}

func fixtureAuditDigest(root string, scopes map[string]scenarioFile) (string, error) {
	directory := filepath.Join(root, "tests/codexinstall")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "impact.go" {
			continue
		}
		omit := false
		for path := range scopes {
			if filepath.Base(path) == name {
				omit = true
				break
			}
		}
		digest, err := canonicalGoDigest(filepath.Join(directory, name), omit)
		if err != nil {
			return "", err
		}
		parts = append(parts, name+"\x00"+digest)
	}
	sort.Strings(parts)
	return byteHash([]byte(strings.Join(parts, "\n"))), nil
}

func scopeSubset(subset, group []string) bool {
	if len(subset) == 0 {
		return false
	}
	for _, value := range subset {
		found := false
		for _, candidate := range group {
			found = found || value == candidate
		}
		if !found {
			return false
		}
	}
	return true
}

func auditedDispatcher(file string, fn *ast.FuncDecl) bool {
	if file == "scenarios.go" && fn.Name.Name == "afterInstall" && fn.Recv != nil && len(fn.Recv.List) == 1 {
		field := fn.Recv.List[0]
		pointer, ok := field.Type.(*ast.StarExpr)
		if !ok || len(field.Names) != 1 || field.Names[0].Name != "j" {
			return false
		}
		kind, ok := pointer.X.(*ast.Ident)
		return ok && kind.Name == "journey"
	}
	if file == "worker.go" && fn.Name.Name == "runWorkerContext" && fn.Recv == nil {
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
