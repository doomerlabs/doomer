package main

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Inspect shell calls, not mentions in filenames, comments or quoted prose.
func invokesDoomerPreflight(command string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	found := false
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		args := make([]string, len(call.Args))
		for i, word := range call.Args {
			args[i] = evidenceLiteral(word.Parts)
		}
		name := filepath.Base(args[0])
		if name == "sh" || name == "bash" || name == "zsh" {
			for i := 1; i+1 < len(args); i++ {
				if strings.HasPrefix(args[i], "-") && strings.Contains(args[i], "c") {
					found = found || invokesDoomerPreflight(args[i+1])
				}
			}
		}
		start := 0
		if name == "env" || name == "command" {
			start = 1
			for start < len(args) && (strings.HasPrefix(args[start], "-") || strings.Contains(args[start], "=")) {
				start++
			}
		}
		if start < len(args) && filepath.Base(args[start]) == "doomer" {
			for i := start + 1; i < len(args); i++ {
				if args[i] == "--api-url" || args[i] == "--profile" {
					i++
					continue
				}
				if strings.HasPrefix(args[i], "--api-url=") || strings.HasPrefix(args[i], "--profile=") {
					continue
				}
				found = found || args[i] == "preflight"
				break
			}
		}
		return true
	})
	return found
}

func evidenceLiteral(parts []syntax.WordPart) string {
	var text strings.Builder
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			text.WriteString(p.Value)
		case *syntax.SglQuoted:
			text.WriteString(p.Value)
		case *syntax.DblQuoted:
			text.WriteString(evidenceLiteral(p.Parts))
		default:
			return ""
		}
	}
	return text.String()
}
