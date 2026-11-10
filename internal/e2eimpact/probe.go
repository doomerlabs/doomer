package e2eimpact

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

// ProbeHandler identifies one inline command callback for a controlled mutation
// experiment. It is not a declaration that an agent cannot execute the callback.
type ProbeHandler struct {
	File     string `json:"file"`
	Function string `json:"function"`
	Field    string `json:"field"`
}

// ReplaceProbeHandler replaces exactly one inline callback body. Everything
// outside that body, including command registration, flags, hooks, imports and
// package initialization, remains byte-for-byte intact. Ambiguity is an error.
// This deliberately does not infer reachability or authorize cached results.
func ReplaceProbeHandler(source []byte, handler ProbeHandler, body string) ([]byte, error) {
	return transformProbeHandler(source, handler, func([]byte) []byte { return []byte(body) })
}

// TrapProbeHandler preserves the original unreachable body so its imports and
// local bindings still compile. Reaching the callback panics before its work.
func TrapProbeHandler(source []byte, handler ProbeHandler, message string) ([]byte, error) {
	return transformProbeHandler(source, handler, func(body []byte) []byte {
		return append([]byte(fmt.Sprintf("{ panic(%q);", message)), body[1:]...)
	})
}

func transformProbeHandler(source []byte, handler ProbeHandler, transform func([]byte) []byte) ([]byte, error) {
	if !cleanPath(handler.File) || handler.Function == "" || (handler.Field != "Run" && handler.Field != "RunE") {
		return nil, fmt.Errorf("invalid probe handler: %+v", handler)
	}
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, handler.File, source, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var matches []*ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != handler.Function {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			// Do not mistake callbacks constructed inside another callback for
			// callbacks belonging to this command constructor.
			if _, nested := node.(*ast.FuncLit); nested {
				return false
			}
			field, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := field.Key.(*ast.Ident)
			if !ok || key.Name != handler.Field {
				return true
			}
			callback, ok := field.Value.(*ast.FuncLit)
			if ok {
				matches = append(matches, callback.Body)
			}
			return false
		})
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("expected one inline %s.%s callback, found %d", handler.Function, handler.Field, len(matches))
	}
	block := matches[0]
	start, end := set.Position(block.Pos()).Offset, set.Position(block.End()).Offset
	body := transform(source[start:end])
	result := make([]byte, 0, len(source)+len(body))
	result = append(result, source[:start]...)
	result = append(result, body...)
	result = append(result, source[end:]...)
	if _, err := parser.ParseFile(token.NewFileSet(), handler.File, result, 0); err != nil {
		return nil, fmt.Errorf("invalid replacement: %w", err)
	}
	return result, nil
}

// ProbeFingerprint is an experimental source-only identity. It must never be
// used as a cache key: it omits execution context and intentionally removes a
// caller-selected callback without proving that callback unreachable.
func ProbeFingerprint(sources map[string][]byte, omitted *ProbeHandler) (string, error) {
	footprint := Footprint{Test: "cli-command-probe"}
	if len(sources) == 0 {
		return "", fmt.Errorf("no captured sources")
	}
	if omitted != nil {
		if _, ok := sources[omitted.File]; !ok {
			return "", fmt.Errorf("missing probe source: %s", omitted.File)
		}
	}
	for name, original := range sources {
		if !cleanPath(name) {
			return "", fmt.Errorf("invalid probe source path: %s", name)
		}
		data := original
		if omitted != nil && name == omitted.File {
			var err error
			data, err = ReplaceProbeHandler(data, *omitted, "{ panic(\"omitted by command probe\") }")
			if err != nil {
				return "", err
			}
		}
		footprint.Dependencies = append(footprint.Dependencies, Dependency{Path: name, SHA256: digest(data)})
	}
	return footprint.Fingerprint(map[string]string{"purpose": "diagnostic-only-not-cache-authority-v1"})
}
