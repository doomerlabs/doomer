package e2eimpact

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"sort"
	"strconv"
	"strings"
)

// AnalyzeGoPackage analyzes one build-selected Go package. Callers must select
// the actual GOOS/GOARCH/build-tag inputs before calling it. Analysis failure is
// an error, never an empty dependency graph that could authorize a cache hit.
// Cross-package calls currently remain unresolved and force broad dependencies.
// Sources also contains the captured bytes of non-Go extra inputs; tracing must
// reject an extra path whose bytes were not captured in this same snapshot.
func AnalyzeGoPackage(importPath string, sources map[string][]byte) (Graph, error) {
	g := Graph{Nodes: map[string]Node{}, Files: map[string][]byte{}}
	set := token.NewFileSet()
	var files []*ast.File
	names := make([]string, 0, len(sources))
	for name, data := range sources {
		if !cleanPath(name) {
			return g, fmt.Errorf("invalid source path %q", name)
		}
		g.Files[name] = append([]byte(nil), data...)
		names = append(names, name)
	}
	sort.Strings(names)
	fileNames := map[*ast.File]string{}
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		file, err := parser.ParseFile(set, name, sources[name], parser.ParseComments)
		if err != nil {
			return g, err
		}
		for _, spec := range file.Imports {
			if spec.Path.Value == `"C"` {
				return g, fmt.Errorf("cgo requires build-aware dependency analysis: %s", name)
			}
		}
		files = append(files, file)
		fileNames[file] = name
	}
	if len(files) == 0 {
		return g, fmt.Errorf("no Go source files")
	}
	info := &types.Info{
		Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
		Types: map[ast.Expr]types.TypeAndValue{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	config := types.Config{Importer: importer.Default()}
	pkg, err := config.Check(importPath, set, files, info)
	if err != nil {
		return g, fmt.Errorf("type-check dependencies: %w", err)
	}
	type declaration struct {
		id   string
		file string
		node ast.Node
	}
	var declarations []declaration
	objects := map[types.Object]string{}
	for _, file := range files {
		name := fileNames[file]
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				object, ok := info.Defs[d.Name].(*types.Func)
				if !ok {
					return g, fmt.Errorf("missing function identity: %s", d.Name.Name)
				}
				id := object.FullName()
				if d.Name.Name == "init" {
					id += "@" + name + ":" + strconv.Itoa(set.Position(d.Pos()).Offset)
					g.Always = append(g.Always, id)
				}
				objects[object] = id
				declarations = append(declarations, declaration{id, name, d})
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					var identifiers []*ast.Ident
					switch s := spec.(type) {
					case *ast.ValueSpec:
						identifiers = s.Names
					case *ast.TypeSpec:
						identifiers = []*ast.Ident{s.Name}
					}
					for _, ident := range identifiers {
						object := info.Defs[ident]
						if object == nil {
							continue
						}
						id := importPath + "." + object.Name()
						if object.Name() == "_" {
							id += "@" + name + ":" + strconv.Itoa(set.Position(ident.Pos()).Offset)
						}
						objects[object] = id
						declarations = append(declarations, declaration{id, name, spec})
						if d.Tok == token.VAR {
							// All package variable initializers execute, including
							// initializers whose values the entry point never reads.
							g.Always = append(g.Always, id)
						}
					}
				}
			}
		}
	}
	for _, decl := range declarations {
		node := Node{File: decl.file}
		if f, ok := decl.node.(*ast.FuncDecl); ok && f.Body == nil {
			node.Unresolved = append(node.Unresolved, "function body is outside Go source")
		}
		refs := map[string]bool{}
		ast.Inspect(decl.node, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok {
				object := info.Uses[ident]
				if id, exists := objects[object]; exists {
					refs[id] = true
				} else if object != nil && object.Pkg() != nil && object.Pkg() != pkg {
					node.Unresolved = append(node.Unresolved, "external declaration: "+object.String())
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || info.Types[call.Fun].IsType() {
				return true
			}
			fun := call.Fun
			for {
				switch f := fun.(type) {
				case *ast.ParenExpr:
					fun = f.X
				case *ast.IndexExpr:
					fun = f.X
				case *ast.IndexListExpr:
					fun = f.X
				default:
					goto resolved
				}
			}
		resolved:
			var object types.Object
			switch f := fun.(type) {
			case *ast.FuncLit:
				return true
			case *ast.Ident:
				object = info.Uses[f]
			case *ast.SelectorExpr:
				object = info.Uses[f.Sel]
				if selection := info.Selections[f]; selection != nil {
					object = selection.Obj()
					if _, ok := selection.Recv().Underlying().(*types.Interface); ok {
						node.Unresolved = append(node.Unresolved, "interface dispatch")
					}
				}
			}
			if _, builtin := object.(*types.Builtin); builtin {
				return true
			}
			if _, known := objects[object]; !known {
				position := set.Position(call.Pos())
				name := "dynamic call"
				if object != nil {
					name = object.String()
				}
				node.Unresolved = append(node.Unresolved, fmt.Sprintf("%s at %s:%d", name, position.Filename, position.Line))
			}
			return true
		})
		for ref := range refs {
			node.References = append(node.References, ref)
		}
		sort.Strings(node.References)
		g.Nodes[decl.id] = node
	}
	for _, file := range files {
		name := fileNames[file]
		for _, spec := range file.Imports {
			id := importPath + ".import@" + name + ":" + spec.Path.Value
			g.Nodes[id] = Node{File: name, Unresolved: []string{"external package initialization: " + spec.Path.Value}}
			g.Always = append(g.Always, id)
		}
		for _, group := range file.Comments {
			for _, comment := range group.List {
				if strings.HasPrefix(comment.Text, "//go:linkname") {
					id := importPath + ".linkname@" + name
					g.Nodes[id] = Node{File: name, Unresolved: []string{"go:linkname bypasses the source call graph"}}
					g.Always = append(g.Always, id)
				}
				if !strings.HasPrefix(comment.Text, "//go:embed ") {
					continue
				}
				// Embeds are package inputs even when an entry does not read
				// the variable. Retaining them all is conservative.
				id := importPath + ".embed@" + name + ":" + strconv.Itoa(set.Position(comment.Pos()).Line)
				node := Node{File: name}
				for _, pattern := range strings.Fields(strings.TrimPrefix(comment.Text, "//go:embed ")) {
					matched := false
					for file := range sources {
						ok, err := matchGlob(path.Join(path.Dir(name), pattern), file)
						if err != nil {
							node.Unresolved = append(node.Unresolved, "unsupported embed pattern: "+pattern)
							break
						}
						if ok {
							fileID := "file:" + file
							g.Nodes[fileID] = Node{File: file}
							node.References = append(node.References, fileID)
							matched = true
						}
					}
					if !matched {
						node.Unresolved = append(node.Unresolved, "unresolved embed: "+pattern)
					}
				}
				g.Nodes[id] = node
				g.Always = append(g.Always, id)
			}
		}
	}
	sort.Strings(g.Always)
	return g, nil
}
