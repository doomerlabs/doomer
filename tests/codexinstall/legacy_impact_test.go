package main

// Historical hand-maintained scopes remain only as a regression baseline for
// the old cache contract. Runtime selection uses scenarioBodyInputs instead.
import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var fixtureScopes = map[string][]string{
	"browser_onboarding.go":  {"browser-onboarding"},
	"unconfigured_repo.go":   {"unconfigured-repo"},
	"upgrade.go":             {"upgrade", "upgrade-defer", "upgrade-untracked", "upgrade-personal", "upgrade-custom", "upgrade-authorized-session"},
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
// call the existing scenario-scoped fixture helpers. Dependency capture and
// shadow analysis add no calls to scenario-scoped journey methods.
const fixtureAuditSHA256 = "e2908371e01ff2b9eba20a4b488421f7e50857ab60cffbadff4d035d0f225382"

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
