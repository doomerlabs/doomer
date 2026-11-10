package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/doomerlabs/doomer/internal/e2eimpact"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// scenarioBodyInputs follows references from executable roots through the real
// dispatchers. It projects only function bodies: declarations, imports, globals
// and compiler directives remain shared. No file-to-scenario list is involved.
// Unknown runtime entry points or mutable scenario identity disable projection.
func scenarioBodyInputs(ctx context.Context, root, snapshot string) (map[string]map[string]string, error) {
	directory := filepath.Join(root, "tests/codexinstall")
	checked, err := e2eimpact.LoadGoSyntax(ctx, root, "./tests/codexinstall")
	if err != nil {
		return nil, err
	}
	configObject, journeyObject := checked.Package.Scope().Lookup("config"), checked.Package.Scope().Lookup("journey")
	if configObject == nil || journeyObject == nil {
		return nil, nil
	}
	configType, configNamed := configObject.Type().(*types.Named)
	journeyType, journeyNamed := journeyObject.Type().(*types.Named)
	if !configNamed || !journeyNamed {
		return nil, nil
	}
	configStruct, configOK := configType.Underlying().(*types.Struct)
	journeyStruct, journeyOK := journeyType.Underlying().(*types.Struct)
	if !configOK || !journeyOK {
		return nil, nil
	}
	identityField, configField := false, false
	for index := 0; index < configStruct.NumFields(); index++ {
		field := configStruct.Field(index)
		identityField = identityField || (field.Name() == "Scenario" && types.Identical(field.Type(), types.Typ[types.String]))
	}
	for index := 0; index < journeyStruct.NumFields(); index++ {
		field := journeyStruct.Field(index)
		configField = configField || (field.Name() == "c" && types.Identical(field.Type(), configType))
	}
	if !identityField || !configField {
		return nil, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	type function struct {
		file string
		decl *ast.FuncDecl
	}
	type edge struct {
		target    string
		scenarios map[string]bool
	}
	functions := map[string]function{}
	definitions := map[string][]string{}
	files := map[string]*ast.File{}
	sources := map[string][]byte{}
	all := map[string]bool{}
	for _, name := range append(append([]string{}, scenarioNames...), "ordinary-upgrade", "policy-activation") {
		all[name] = true
	}
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
		file := checked.Files[name]
		if file == nil {
			continue
		}
		files[name], sources[name] = file, raw
		for _, spec := range file.Imports {
			switch strings.Trim(spec.Path.Value, "\"") {
			case "reflect", "unsafe", "plugin", "C":
				// DeepEqual is the only reflection operation used by this runner.
				if spec.Path.Value != `"reflect"` {
					opaque = true
				}
			}
		}
		for _, comments := range file.Comments {
			for _, comment := range comments.List {
				if strings.HasPrefix(comment.Text, "//go:linkname") {
					opaque = true
				}
			}
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				id := name + ":" + fn.Name.Name
				// A duplicate name is intentionally ambiguous. References below
				// reach every matching receiver rather than guessing a target.
				if _, exists := functions[id]; exists {
					id += fmt.Sprint(fn.Pos())
				}
				functions[id] = function{name, fn}
				definitions[fn.Name.Name] = append(definitions[fn.Name.Name], id)
				if fn.Body == nil {
					opaque = true
				}
			}
		}
		if scenarioMutation(file, checked) {
			opaque = true
		}

	}
	if opaque {
		return nil, nil
	}
	edges := map[string][]edge{}
	reached := map[string]map[string]bool{}
	seed := func(id string, group map[string]bool) bool {
		if reached[id] == nil {
			reached[id] = map[string]bool{}
		}
		changed := false
		for scenario := range group {
			if !reached[id][scenario] {
				reached[id][scenario] = true
				changed = true
			}
		}
		return changed
	}
	// Every named function value escaping direct invocation remains shared.
	// This includes callbacks passed through channels, interfaces or variables.
	direct := map[token.Pos]bool{}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					direct[fn.Pos()] = true
				case *ast.SelectorExpr:
					direct[fn.Sel.Pos()] = true
				}
			}
			return true
		})
	}
	for id, fn := range functions {
		if fn.decl.Name.Name == "main" || fn.decl.Name.Name == "init" || unicode.IsUpper([]rune(fn.decl.Name.Name)[0]) {
			seed(id, all)
		}
		check := func(ref *ast.Ident, group []string, _ bool) {
			if ref.Pos() == fn.decl.Name.Pos() {
				return
			}
			mask := all
			if group != nil {
				mask = map[string]bool{}
				for _, name := range group {
					mask[name] = true
				}
			}
			for _, target := range definitions[ref.Name] {
				edges[id] = append(edges[id], edge{target, mask})
				if !direct[ref.Pos()] {
					seed(target, all)
				}
			}
		}
		ast.Walk(scopeVisitor{refine: auditedDispatcher(fn.file, fn.decl), check: check}, fn.decl.Body)
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			if _, ok := decl.(*ast.FuncDecl); ok {
				continue
			}
			ast.Inspect(decl, func(node ast.Node) bool {
				if ref, ok := node.(*ast.Ident); ok {
					for _, target := range definitions[ref.Name] {
						seed(target, all)
					}
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for source, links := range edges {
			for _, link := range links {
				mask := map[string]bool{}
				for scenario := range reached[source] {
					if link.scenarios[scenario] {
						mask[scenario] = true
					}
				}
				changed = seed(link.target, mask) || changed
			}
		}
	}
	result := map[string]map[string]string{}
	for name, raw := range sources {
		relative, err := filepath.Rel(snapshot, filepath.Join(directory, name))
		if err != nil {
			return nil, err
		}
		hashes := map[string]string{}
		for scenario := range all {
			file, err := parser.ParseFile(token.NewFileSet(), name, raw, parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				// Duplicate receiver names share the union of all their scopes.
				used := false
				for _, id := range definitions[fn.Name.Name] {
					if functions[id].file == name {
						used = used || reached[id][scenario]
					}
				}
				if !used {
					fn.Body = nil
				} else if auditedDispatcher(name, fn) {
					projectScenarioBranches(fn.Body, scenario)
				}
			}
			var canonical bytes.Buffer
			if err := format.Node(&canonical, token.NewFileSet(), file); err != nil {
				return nil, err
			}
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "//go:") || strings.HasPrefix(line, "// +build") || strings.HasPrefix(line, "//line ") {
					canonical.WriteString("\n" + line)
				}
			}
			hashes[scenario] = byteHash(canonical.Bytes())
		}
		// Shared files retain their original content hash. Store projections
		// only when the real scenarios have different reachable bodies.
		first, different := "", false
		for _, hash := range hashes {
			if first == "" {
				first = hash
			}
			different = different || hash != first
		}
		if different {
			result[filepath.ToSlash(relative)] = hashes
		}
	}
	return result, nil
}

// A scenario can be copied into a fresh worker, but it cannot change after
// dispatch. Typed checks catch aliases rather than just variables named c/j.
func scenarioMutation(file *ast.File, checked e2eimpact.CheckedSyntax) bool {
	protected := func(t types.Type) bool {
		if pointer, ok := t.(*types.Pointer); ok {
			t = pointer.Elem()
		}
		named, ok := t.(*types.Named)
		return ok && named.Obj().Pkg() == checked.Package && (named.Obj().Name() == "config" || named.Obj().Name() == "journey")
	}
	pointer := func(expr ast.Expr) bool {
		t, ok := checked.Info.TypeOf(expr).(*types.Pointer)
		return ok && protected(t.Elem())
	}
	field := func(expr ast.Expr) bool {
		selector, ok := expr.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		selection := checked.Info.Selections[selector]
		if selection == nil {
			return false
		}
		return protected(selection.Recv()) && (selection.Obj().Name() == "Scenario" || selection.Obj().Name() == "c")
	}
	opaque := false
	for _, decl := range file.Decls {
		fn, isFunction := decl.(*ast.FuncDecl)
		main := isFunction && fn.Recv == nil && fn.Name.Name == "main"
		if isFunction && auditedDispatcher(filepath.Base(checkedFileName(checked, file)), fn) {
			identity := "j"
			if fn.Recv == nil {
				identity = "c"
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if id, ok := node.(*ast.Ident); ok && id.Name == identity && checked.Info.Defs[id] != nil {
					opaque = true
				}
				if assignment, ok := node.(*ast.AssignStmt); ok {
					for _, lhs := range assignment.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && id.Name == identity {
							opaque = true
						}
					}
				}
				return true
			})
		}
		ast.Inspect(decl, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.BranchStmt:
				if n.Tok == token.FALLTHROUGH {
					opaque = true
				}
			case *ast.UnaryExpr:
				if n.Op == token.AND && field(n.X) {
					opaque = true
				}
			case *ast.AssignStmt:
				for index, lhs := range n.Lhs {
					if field(lhs) || (n.Tok != token.DEFINE && protected(checked.Info.TypeOf(lhs))) {
						opaque = true
					}
					if n.Tok == token.DEFINE && protected(checked.Info.TypeOf(lhs)) && !main {
						// Only the worker's direct journey{c:c} construction binds
						// a new identity. Copies/conversions could shadow a dispatcher.
						valid := isFunction && fn.Recv == nil && fn.Name.Name == "runWorkerContext" && len(n.Rhs) == len(n.Lhs)
						if valid {
							literal, ok := n.Rhs[index].(*ast.CompositeLit)
							valid = ok
							found := false
							if ok {
								for _, item := range literal.Elts {
									pair, ok := item.(*ast.KeyValueExpr)
									if !ok {
										continue
									}
									key, ok := pair.Key.(*ast.Ident)
									if !ok || key.Name != "c" {
										continue
									}
									value, ok := pair.Value.(*ast.Ident)
									found = ok && value.Name == "c"
								}
							}
							valid = valid && found
						}
						if !valid && len(n.Rhs) == len(n.Lhs) {
							rhs := n.Rhs[index]
							named, ok := checked.Info.TypeOf(rhs).(*types.Named)
							copy := ok && named.Obj().Pkg() == checked.Package && named.Obj().Name() == "config"
							switch rhs.(type) {
							case *ast.Ident, *ast.SelectorExpr:
								valid = copy
							}
						}
						if !valid {
							opaque = true
						}
					}
				}
				for _, rhs := range n.Rhs {
					if pointer(rhs) {
						opaque = true
					}
				}
			case *ast.ValueSpec:
				for _, name := range n.Names {
					if protected(checked.Info.TypeOf(name)) && !main {
						opaque = true
					}
				}
				for _, value := range n.Values {
					if pointer(value) {
						opaque = true
					}
				}
			case *ast.SendStmt:
				if pointer(n.Value) {
					opaque = true
				}
			case *ast.ReturnStmt:
				for _, value := range n.Results {
					if pointer(value) {
						opaque = true
					}
				}
			case *ast.CompositeLit:
				if protected(checked.Info.TypeOf(n)) {
					for _, item := range n.Elts {
						pair, ok := item.(*ast.KeyValueExpr)
						if !ok {
							opaque = true
							continue
						}
						key, ok := pair.Key.(*ast.Ident)
						if !ok {
							opaque = true
							continue
						}
						if key.Name == "Scenario" {
							star, ok := pair.Value.(*ast.StarExpr)
							valid := main && ok
							if valid {
								id, ok := star.X.(*ast.Ident)
								valid = ok && id.Name == "scenario"
							}
							if !valid {
								opaque = true
							}
						}
						if key.Name == "c" {
							value, ok := pair.Value.(*ast.Ident)
							if !isFunction || fn.Name.Name != "runWorkerContext" || !ok || value.Name != "c" {
								opaque = true
							}
						}
					}
				}
				for _, item := range n.Elts {
					if pair, ok := item.(*ast.KeyValueExpr); ok {
						if pointer(pair.Value) {
							opaque = true
						}
					} else if expr, ok := item.(ast.Expr); ok && pointer(expr) {
						opaque = true
					}
				}
			case *ast.CallExpr:
				var object types.Object
				switch call := n.Fun.(type) {
				case *ast.Ident:
					object = checked.Info.Uses[call]
				case *ast.SelectorExpr:
					object = checked.Info.Uses[call.Sel]
				}
				function, known := object.(*types.Func)
				if known && function.Pkg() != nil && function.Pkg().Path() == "reflect" && function.Name() != "DeepEqual" {
					opaque = true
				}
				for _, arg := range n.Args {
					if !pointer(arg) {
						continue
					}
					// The initial worker JSON decoder is checked by the report's scenario
					// identity before cache acceptance. Later external writes are forbidden.
					initialDecode := main && known && function.Pkg() != nil && function.Pkg().Path() == "encoding/json" && function.Name() == "Decode"
					if !initialDecode && (!known || function.Pkg() != checked.Package) {
						opaque = true
					}
				}
			}
			return true
		})
	}
	return opaque
}

func checkedFileName(checked e2eimpact.CheckedSyntax, file *ast.File) string {
	for name, candidate := range checked.Files {
		if candidate == file {
			return name
		}
	}
	return ""
}

func projectScenarioBranches(body *ast.BlockStmt, scenario string) {
	ast.Inspect(body, func(node ast.Node) bool {
		if statement, ok := node.(*ast.SwitchStmt); ok && scenarioExpression(statement.Tag) {
			for _, item := range statement.Body.List {
				clause := item.(*ast.CaseClause)
				matches, unknown := false, len(clause.List) == 0
				for _, expr := range clause.List {
					lit, ok := expr.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						unknown = true
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						unknown = true
					} else {
						matches = matches || value == scenario
					}
				}
				if !matches && !unknown {
					clause.Body = nil
				}
			}
		}
		if statement, ok := node.(*ast.IfStmt); ok {
			if value, known := scenarioCondition(statement.Cond); known {
				if value == scenario {
					statement.Else = nil
				} else {
					statement.Body.List = nil
				}
			}
		}
		return true
	})
}
