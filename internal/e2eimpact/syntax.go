package e2eimpact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// CheckedSyntax binds every identifier to compiler-selected package types.
// Consumers can distinguish configuration fields from unrelated fields with
// the same name instead of relying on source spelling.
type CheckedSyntax struct {
	Package *types.Package
	Info    *types.Info
	Files   map[string]*ast.File
}

func LoadGoSyntax(ctx context.Context, root, target string) (CheckedSyntax, error) {
	var out CheckedSyntax
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-export", "-json", "-mod=readonly", target)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	raw, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("load typed scenario inputs: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	exports := map[string]string{}
	var selected struct {
		ImportPath, Dir, Export string
		GoFiles                 []string
		DepOnly                 bool
	}
	found := false
	for {
		var pkg struct {
			ImportPath, Dir, Export string
			GoFiles                 []string
			DepOnly                 bool
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return out, err
		}
		exports[pkg.ImportPath] = pkg.Export
		if !pkg.DepOnly {
			if found {
				return out, fmt.Errorf("expected one scenario package")
			}
			selected = pkg
			found = true
		}
	}
	if !found {
		return out, fmt.Errorf("scenario package not found")
	}
	set := token.NewFileSet()
	out.Files = map[string]*ast.File{}
	var files []*ast.File
	for _, name := range selected.GoFiles {
		file, err := parser.ParseFile(set, filepath.Join(selected.Dir, name), nil, parser.ParseComments)
		if err != nil {
			return out, err
		}
		out.Files[name] = file
		files = append(files, file)
	}
	out.Info = &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	config := types.Config{Importer: importer.ForCompiler(set, "gc", func(path string) (io.ReadCloser, error) {
		if exports[path] == "" {
			return nil, fmt.Errorf("missing export data: %s", path)
		}
		return os.Open(exports[path])
	})}
	out.Package, err = config.Check(selected.ImportPath, set, files, out.Info)
	return out, err
}
