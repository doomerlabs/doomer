package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"mvdan.cc/sh/v3/syntax"
)

// Only recognize actual Doomer invocations, never mentions in echo/comments.
func resetDoomerCommand(command string) bool {
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "hook")
	if err != nil {
		return false
	}
	found := false
	syntax.Walk(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		args := []string{}
		for _, a := range call.Args {
			args = append(args, resetShellLiteral(a.Parts))
		}
		for i, arg := range args {
			if i != 0 && args[0] != "env" {
				continue
			}
			name := filepath.Base(arg)
			if name != "doomer" && name != "doomer-beta" {
				continue
			}
			rest := " " + strings.Join(args[i+1:], " ") + " "
			if strings.Contains(rest, " agent-hook ") || strings.Contains(rest, " watch hook ") {
				found = true
			}
		}
		return true
	})
	return found
}

// Return the original hook identities so only their trust records are removed.
func resetHookGroups(hooks map[string]any, file string) ([]string, int, error) {
	var identities []string
	count := 0
	for event, raw := range hooks {
		if event == "state" {
			continue
		}
		groups, ok := raw.([]any)
		if !ok {
			return nil, 0, fmt.Errorf("unsupported hook layout: %s", event)
		}
		kept := []any{}
		for gi, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				return nil, 0, fmt.Errorf("invalid hook group")
			}
			handlers, ok := group["hooks"].([]any)
			if !ok {
				return nil, 0, fmt.Errorf("invalid hook handlers")
			}
			remaining := []any{}
			for hi, rawHandler := range handlers {
				handler, ok := rawHandler.(map[string]any)
				if !ok {
					return nil, 0, fmt.Errorf("invalid hook handler")
				}
				command, _ := handler["command"].(string)
				if resetDoomerCommand(command) {
					normalized := strings.ToLower(event)
					switch event {
					case "UserPromptSubmit":
						normalized = "user_prompt_submit"
					case "SessionStart":
						normalized = "session_start"
					case "PreToolUse":
						normalized = "pre_tool_use"
					case "PostToolUse":
						normalized = "post_tool_use"
					case "PostToolUseFailure":
						normalized = "post_tool_use_failure"
					}
					identities = append(identities, fmt.Sprintf("%s:%s:%d:%d", file, normalized, gi, hi))
					count++
				} else {
					remaining = append(remaining, rawHandler)
				}
			}
			if len(remaining) > 0 {
				group["hooks"] = remaining
				kept = append(kept, group)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	return identities, count, nil
}

// Replaces one TOML table tree, preserving all unrelated bytes and comments.
func resetPatchTable(data []byte, root string, value map[string]any) ([]byte, error) {
	var p unstable.Parser
	p.Reset(data)
	type span struct{ start, end int }
	spans := []span{}
	active := -1
	for p.NextExpression() {
		n := p.Expression()
		if n.Kind != unstable.Table && n.Kind != unstable.ArrayTable {
			continue
		}
		key := []string{}
		it := n.Key()
		for it.Next() {
			key = append(key, string(it.Node().Data))
		}
		pos := int(n.Child().Raw.Offset)
		start := bytes.LastIndexByte(data[:pos], '\n') + 1
		if active >= 0 {
			spans[active].end = start
			active = -1
		}
		if len(key) > 0 && key[0] == root {
			spans = append(spans, span{start, len(data)})
			active = len(spans) - 1
		}
	}
	if err := p.Error(); err != nil {
		return nil, err
	}
	// Dotted/inline root definitions cannot safely be patched with table spans.
	var decoded map[string]any
	if err := toml.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if _, exists := decoded[root]; exists && len(spans) == 0 {
		return nil, fmt.Errorf("unsupported inline %s layout; manual cleanup required", root)
	}
	var out []byte
	offset := 0
	for _, s := range spans {
		out = append(out, data[offset:s.start]...)
		offset = s.end
	}
	out = append(out, data[offset:]...)
	if len(value) > 0 {
		encoded, err := toml.Marshal(map[string]any{root: value})
		if err != nil {
			return nil, err
		}
		out = append(out, '\n')
		out = append(out, encoded...)
	}
	var check map[string]any
	if err := toml.Unmarshal(out, &check); err != nil {
		return nil, err
	}
	return out, nil
}

func resetRemovePolicies(policy string) (string, int) {
	const heading = "Doomer repository review authorization\n"
	count := 0
	for offset := 0; offset < len(policy); {
		i := strings.Index(policy[offset:], heading)
		if i < 0 {
			break
		}
		i += offset
		lines := strings.SplitN(policy[i:], "\n", 4)
		if len(lines) < 4 {
			break
		}
		var scope struct {
			Account, Repository, Service, Profile, Agent string
			AllRepositories                              bool `json:"all_repositories"`
		}
		if json.Unmarshal([]byte(lines[2]), &scope) == nil {
			exact := proposedCodexReviewPolicy(managedConsentEvidence{Account: scope.Account, Repository: scope.Repository, Service: scope.Service, Profile: scope.Profile, Agent: scope.Agent, AllRepositories: scope.AllRepositories}).ExtraPolicy
			if strings.HasPrefix(policy[i:], exact) {
				policy = policy[:i] + policy[i+len(exact):]
				count++
				offset = i
				continue
			}
		}
		offset = i + len(heading)
	}
	return policy, count
}

func resetReadFile(path string) ([]byte, os.FileMode, error) {
	// Reject symlinked parents as well as leaves. Cleanup must not escape a
	// discovered home through a repository-controlled directory.
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, 0, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if runtime.GOOS == "darwin" && (p == "/var" || p == "/tmp") {
				if target, err := filepath.EvalSymlinks(p); err == nil && target == "/private"+p {
					continue
				}
			}
			return nil, 0, fmt.Errorf("refusing symlink: %s", p)
		}
		if p == path && !info.Mode().IsRegular() {
			return nil, 0, fmt.Errorf("not a regular file: %s", p)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	data, err := os.ReadFile(path)
	return data, info.Mode().Perm(), err
}

func resetWriteFile(path string, before, after []byte, mode os.FileMode) error {
	current, _, err := resetReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) {
		return fmt.Errorf("file changed during reset: %s", path)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".doomer-reset-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(after)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	current, _, err = resetReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) {
		return fmt.Errorf("file changed during reset: %s", path)
	}
	return os.Rename(f.Name(), path)
}

func resetShellLiteral(parts []syntax.WordPart) string {
	var out strings.Builder
	for _, part := range parts {
		switch v := part.(type) {
		case *syntax.Lit:
			out.WriteString(v.Value)
		case *syntax.SglQuoted:
			out.WriteString(v.Value)
		case *syntax.DblQuoted:
			out.WriteString(resetShellLiteral(v.Parts))
		default:
			return ""
		}
	}
	return out.String()
}

func resetCheckDirectory(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if runtime.GOOS == "darwin" && (p == "/var" || p == "/tmp") {
				if target, err := filepath.EvalSymlinks(p); err == nil && target == "/private"+p {
					continue
				}
			}
			return fmt.Errorf("not a regular directory: %s", p)
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}
