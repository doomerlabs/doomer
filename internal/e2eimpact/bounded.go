package e2eimpact

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path"
	"strconv"
)

// BoundedHandlerSource builds a deliberately restricted test artifact. Unlike
// ProbeFingerprint, its output must actually be compiled and executed before
// recording a success. Cache identity must include the resulting binary, not
// just a prediction of which source the command might use.
//
// Imports remain present for their initialization effects. An import used only
// by the removed body becomes a blank import. Unsupported import naming causes
// a compile error; callers must fall back to the full build, never guess a hit.
func BoundedHandlerSource(source []byte, handler ProbeHandler) ([]byte, error) {
	replaced, err := ReplaceProbeHandler(source, handler, `{ panic("command outside bounded CLI smoke contract") }`)
	if err != nil {
		return nil, err
	}
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, handler.File, replaced, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if id, ok := selector.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	for _, spec := range file.Imports {
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		} else {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("decode import: %w", err)
			}
			name = path.Base(importPath)
		}
		if name != "_" && name != "." && !used[name] {
			spec.Name = ast.NewIdent("_")
		}
	}
	var out bytes.Buffer
	if err := format.Node(&out, set, file); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
