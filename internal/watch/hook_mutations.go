package watch

import (
	"path/filepath"
	"strings"
	"text/scanner"

	"mvdan.cc/sh/v3/syntax"
)

// Classify syntax, not text: arguments, comments and heredoc data can mention
// editing commands without executing them. Walking the tree still finds writes
// in pipelines, compound commands and command/process substitutions.
func shellMutates(command string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		// Unknown commands cannot claim ownership of another chat's edits.
		return false
	}
	mutating := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if mutating {
			return false
		}
		switch n := node.(type) {
		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll:
				mutating = true
			}
		case *syntax.CallExpr:
			args := make([]string, len(n.Args))
			for i, word := range n.Args {
				args[i] = shellLiteral(word.Parts)
			}
			mutating = shellCallMutates(args)
		case *syntax.Stmt:
			// Interpreter stdin is code; other heredoc bodies remain data.
			if call, ok := n.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
				name := filepath.Base(shellLiteral(call.Args[0].Parts))
				for _, redirect := range n.Redirs {
					if redirect.Hdoc == nil {
						continue
					}
					body := command[redirect.Hdoc.Pos().Offset():redirect.Hdoc.End().Offset()]
					switch name {
					case "sh", "bash", "dash", "zsh", "ksh":
						mutating = mutating || shellMutates(body)
					default:
						mutating = mutating || scriptInterpreter(name) && scriptMutates(body)
					}
				}
			}
		}
		return !mutating
	})
	return mutating
}

// Only statically known words are used as executable names or options. This
// unwraps quotes without evaluating expansions or executing any user input.
func shellLiteral(parts []syntax.WordPart) string {
	value, _ := shellLiteralParts(parts)
	return value
}

func shellLiteralParts(parts []syntax.WordPart) (string, bool) {
	var value strings.Builder
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			value.WriteString(p.Value)
		case *syntax.SglQuoted:
			value.WriteString(p.Value)
		case *syntax.DblQuoted:
			quoted, ok := shellLiteralParts(p.Parts)
			if !ok {
				return "", false
			}
			value.WriteString(quoted)
		default:
			return "", false
		}
	}
	return value.String(), true
}

func shellCallMutates(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name, rest := filepath.Base(args[0]), args[1:]
	switch name {
	case "rm", "mv", "cp", "tee", "apply_patch":
		return true
	case "sudo", "env", "command", "exec", "nohup":
		for i := 0; i < len(rest); i++ {
			arg := rest[i]
			if arg == "-u" || arg == "--user" || arg == "-g" || arg == "--group" || arg == "--unset" || arg == "-C" || arg == "--chdir" {
				i++
				continue
			}
			if arg == "--" {
				return shellCallMutates(rest[i+1:])
			}
			if arg != "" && !strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") {
				return shellCallMutates(rest[i:])
			}
		}
	case "git":
		for i := 0; i < len(rest); i++ {
			arg := rest[i]
			if arg == "-C" || arg == "-c" || arg == "--git-dir" || arg == "--work-tree" || arg == "--namespace" || arg == "--config-env" {
				i++
				continue
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			switch arg {
			case "apply", "restore", "checkout", "switch", "reset", "cherry-pick", "merge", "rebase":
				return true
			}
			return false
		}
	case "go":
		return len(rest) > 0 && (rest[0] == "get" || len(rest) > 1 && rest[0] == "mod" && (rest[1] == "tidy" || rest[1] == "edit"))
	case "sed", "perl":
		for i := 0; i < len(rest); i++ {
			arg := rest[i]
			if arg == "-e" || arg == "-f" || arg == "--expression" || arg == "--file" {
				i++
				continue
			}
			if strings.HasPrefix(arg, "-e") || strings.HasPrefix(arg, "-f") || strings.HasPrefix(arg, "--expression=") || strings.HasPrefix(arg, "--file=") {
				continue
			}
			if arg == "--" || !strings.HasPrefix(arg, "-") {
				break
			}
			if arg == "--in-place" || strings.HasPrefix(arg, "--in-place=") || !strings.HasPrefix(arg, "--") && strings.Contains(arg, "i") {
				return true
			}
		}
	case "sh", "bash", "dash", "zsh", "ksh":
		for i, arg := range rest {
			if strings.HasPrefix(arg, "-") && strings.Contains(arg, "c") && i+1 < len(rest) {
				return shellMutates(rest[i+1])
			}
		}
	default:
		if scriptInterpreter(name) {
			for i, arg := range rest {
				if (arg == "-c" || arg == "-e" || arg == "--eval") && i+1 < len(rest) {
					return scriptMutates(rest[i+1])
				}
			}
		}
	}
	return false
}

func scriptInterpreter(name string) bool {
	name = filepath.Base(name)
	return name == "python" || strings.HasPrefix(name, "python3") || name == "node" || name == "nodejs"
}

// Preserve recognition of explicit Python/JavaScript file writes without
// interpreting strings or comments containing example code as operations.
func scriptMutates(code string) bool {
	var lex scanner.Scanner
	lex.Init(strings.NewReader(code))
	lex.Mode = scanner.ScanIdents | scanner.ScanStrings | scanner.ScanRawStrings | scanner.ScanChars | scanner.ScanComments | scanner.SkipComments
	lex.Error = func(*scanner.Scanner, string) {}
	previous := rune(0)
	for token := lex.Scan(); token != scanner.EOF; token = lex.Scan() {
		if token == '#' {
			for lex.Peek() != '\n' && lex.Peek() != scanner.EOF {
				lex.Next()
			}
		}
		if token == scanner.Ident {
			name := lex.TokenText()
			write := name == "apply_patch" || name == "writeFile" || name == "writeFileSync" || previous == '.' && (name == "write_text" || name == "write_bytes")
			if write && lex.Scan() == '(' {
				return true
			}
		}
		previous = token
	}
	return false
}
