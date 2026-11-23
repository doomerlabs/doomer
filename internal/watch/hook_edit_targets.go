package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// HookEditRepositories finds repositories named by a pre-tool edit. Resolution
// is local and never executes the tool command. The live watcher checks consent
// before attaching each repository.
func HookEditRepositories(ctx context.Context, input HookInput) []string {
	if !filepath.IsAbs(input.CWD) {
		return nil
	}
	var args struct {
		Command  string `json:"command"`
		Cmd      string `json:"cmd"`
		Workdir  string `json:"workdir"`
		CWD      string `json:"cwd"`
		Patch    string `json:"patch"`
		Input    string `json:"input"`
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	var raw string
	if json.Unmarshal(input.Input, &args) != nil && json.Unmarshal(input.Input, &raw) != nil {
		return nil
	}
	cwd := input.CWD
	explicit := args.Workdir
	if explicit == "" {
		explicit = args.CWD
	}
	if explicit != "" {
		cwd = hookTargetPath(cwd, explicit)
	}
	command := args.Command
	if command == "" {
		command = args.Cmd
	}
	if command == "" {
		command = raw
	}
	var targets []string
	name := strings.ToLower(input.Tool)
	switch {
	case name == "apply_patch":
		patch := args.Patch
		if patch == "" {
			patch = args.Input
		}
		if patch == "" {
			patch = command
		}
		for _, line := range strings.Split(patch, "\n") {
			for _, marker := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: ", "*** Move to: "} {
				if path, ok := strings.CutPrefix(line, marker); ok && path != "" {
					targets = append(targets, strings.TrimSuffix(path, "\r"))
					break
				}
			}
		}
	case command != "":
		cwd, command = hookShellDirectory(cwd, command)
		targets = shellWritePaths(command)
		if len(targets) == 0 && shellMutates(command) {
			targets = []string{cwd}
		}
	case mutates(input):
		for _, path := range []string{args.Path, args.FilePath} {
			if path != "" {
				targets = append(targets, path)
			}
		}
	}
	var repos []string
	seen := map[string]bool{}
	for _, target := range targets {
		if repo := hookTargetRepository(ctx, hookTargetPath(cwd, target)); repo != "" && !seen[repo] {
			seen[repo] = true
			repos = append(repos, repo)
		}
	}
	return repos
}

// A literal leading cd guarded by && supplies the working directory for the
// remaining write command. Other directory changes leave the shell unresolved.
func hookShellDirectory(cwd, command string) (string, string) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return cwd, command
	}
	stmt := file.Stmts[0]
	for {
		branch, ok := stmt.Cmd.(*syntax.BinaryCmd)
		if !ok || branch.Op != syntax.AndStmt || stmt.Negated || stmt.Background || len(stmt.Redirs) != 0 {
			return cwd, command
		}
		stmt = branch.X
		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok {
			continue
		}
		if stmt.Negated || stmt.Background || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) != 2 || shellLiteral(call.Args[0].Parts) != "cd" {
			return cwd, command
		}
		dir, literal := shellWritePath(call.Args[1])
		if !literal || !(filepath.IsAbs(dir) || strings.HasPrefix(dir, "./") || strings.HasPrefix(dir, "../")) {
			return cwd, command
		}
		return hookTargetPath(cwd, dir), command[branch.Y.Pos().Offset():]
	}
}

func hookTargetPath(cwd, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

func hookTargetRepository(ctx context.Context, path string) string {
	for ctx.Err() == nil {
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				path = filepath.Dir(path)
			}
			repo, _ := Root(ctx, path)
			return repo
		}
		if !os.IsNotExist(err) || filepath.Dir(path) == path {
			break
		}
		path = filepath.Dir(path)
	}
	return ""
}
