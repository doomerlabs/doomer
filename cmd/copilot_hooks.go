package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

const copilotHookFilename = "doomer-node.json"
const copilotProjectHookPath = ".github/hooks/" + copilotHookFilename
const copilotStopPrefix = "Doomer Stop hook continuation. "

func excludeCopilotProjectHook(file string) error {
	github := filepath.Dir(filepath.Dir(file))
	if filepath.Base(github) != ".github" || filepath.Base(filepath.Dir(file)) != "hooks" {
		return nil
	}
	repo := filepath.Dir(github)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	git := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).Output()
	}
	root, err := watching.Root(ctx, repo)
	if err != nil || root != repo {
		return fmt.Errorf("Copilot project hook must be at the Git checkout root: %s", file)
	}
	for _, path := range []string{github, filepath.Dir(file)} {
		info, err := os.Lstat(path)
		if !errors.Is(err, os.ErrNotExist) && (err != nil || !info.IsDir()) {
			return fmt.Errorf("Copilot project hook directory must be a local directory: %s", path)
		}
	}
	info, err := os.Lstat(file)
	if !errors.Is(err, os.ErrNotExist) && (err != nil || !info.Mode().IsRegular()) {
		return fmt.Errorf("Copilot project hook must be a regular file: %s", file)
	}
	tracked, err := git("ls-files", "--", copilotProjectHookPath, copilotProjectHookPath+".before-doomer-node-*")
	if err != nil {
		return fmt.Errorf("check Copilot project hook paths: %w", err)
	}
	if len(tracked) != 0 {
		return fmt.Errorf("Copilot project hook path is already tracked: %s", file)
	}
	path, err := git("rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("find Git's local exclude file: %w", err)
	}
	exclude := strings.TrimSpace(string(path))
	if !filepath.IsAbs(exclude) {
		return fmt.Errorf("Git did not return an absolute local exclude path")
	}
	unlock, err := npmSetupLock(exclude)
	if err != nil {
		return err
	}
	defer unlock()
	data, err := os.ReadFile(exclude)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The suffix also covers Doomer lock files and configuration backups.
	pattern := "/" + copilotProjectHookPath + "*"
	if !strings.Contains("\n"+string(data)+"\n", "\n"+pattern+"\n") {
		data = append(data, []byte("\n"+pattern+"\n")...)
		if err := writeNPMFile(exclude, data, 0600); err != nil {
			return err
		}
	}
	if _, err := git("check-ignore", "--quiet", "--", copilotProjectHookPath); err != nil {
		return fmt.Errorf("Git does not ignore the local Copilot hook: %s", file)
	}
	if _, err := git("check-ignore", "--quiet", "--", copilotProjectHookPath+".before-doomer-node-copy"); err != nil {
		return fmt.Errorf("Git does not ignore local Copilot hook backups: %s", file)
	}
	return nil
}

var copilotHookEvents = []struct {
	name, event string
	timeout     int
}{
	{"userPromptSubmitted", "UserPromptSubmit", 45},
	{"preToolUse", "PreToolUse", 25},
	{"postToolUse", "PostToolUse", 10},
	{"agentStop", "Stop", 60},
	{"sessionEnd", "SessionEnd", 15},
}

func copilotHome(home string) string {
	if profile := os.Getenv("COPILOT_HOME"); profile != "" {
		return profile
	}
	return filepath.Join(home, ".copilot")
}

func npmValidateAgentHooks(config npmJSONObject, agent string) (npmJSONObject, error) {
	if agent != "copilot" {
		return npmValidateHooks(config)
	}
	if version, exists := config["version"]; exists && version != json.Number("1") {
		return nil, fmt.Errorf("unsupported Copilot hook configuration version")
	}
	value, exists := config["hooks"]
	if !exists {
		return npmJSONObject{}, nil
	}
	hooks, ok := value.(npmJSONObject)
	if !ok {
		return nil, fmt.Errorf("invalid Copilot hook settings")
	}
	for _, event := range copilotHookEvents {
		if entries, exists := hooks[event.name]; exists {
			if _, valid := npmObjects(entries); !valid {
				return nil, fmt.Errorf("invalid %s hook handlers", event.name)
			}
		}
	}
	return hooks, nil
}

func mergeCopilotHooks(hooks npmJSONObject, owner, command string) bool {
	changed := false
	for _, event := range copilotHookEvents {
		old, _ := hooks[event.name].([]any)
		kept := make([]any, 0, len(old)+1)
		handler := npmJSONObject{"type": "command", "command": command + " --event=" + event.name, "timeoutSec": json.Number(strconv.Itoa(event.timeout))}
		inserted := false
		for _, entry := range old {
			if !npmOwnsHook(entry.(npmJSONObject), owner) {
				kept = append(kept, entry)
			} else if !inserted {
				kept = append(kept, handler)
				inserted = true
			}
		}
		if !inserted {
			kept = append(kept, handler)
		}
		if !reflect.DeepEqual(old, kept) {
			hooks[event.name] = kept
			changed = true
		}
	}
	return changed
}

func decodeCopilotHook(raw []byte, event string) (watching.HookInput, string, error) {
	var p struct {
		SessionID      string          `json:"sessionId"`
		AgentID        string          `json:"agentId"`
		CWD            string          `json:"cwd"`
		Prompt         string          `json:"prompt"`
		ToolName       string          `json:"toolName"`
		ToolArgs       json.RawMessage `json:"toolArgs"`
		StopActive     bool            `json:"stop_hook_active"`
		TranscriptPath string          `json:"transcriptPath"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return watching.HookInput{}, "", err
	}
	for _, entry := range copilotHookEvents {
		if entry.name == event {
			input := watching.HookInput{Session: p.SessionID, CWD: p.CWD, Event: entry.event, Prompt: p.Prompt, Tool: p.ToolName, Input: p.ToolArgs, StopActive: p.StopActive}
			switch input.Tool {
			case "create":
				input.Tool = "write"
			case "str_replace_editor":
				input.Tool = "edit"
			}
			// Child tools can register edit targets on the session's watcher.
			// Only the root agent owns its lifecycle and review handoff.
			if p.AgentID != "" && input.Event != "PreToolUse" {
				input.Event = ""
			}
			return input, p.TranscriptPath, nil
		}
	}
	return watching.HookInput{}, "", fmt.Errorf("unsupported Copilot hook event")
}

// Copilot supplies a transcript path in agentStop instead of the final reply.
// Read a bounded tail from the current session's regular event log only.
func copilotTurnReply(session, path string) (reply string) {
	home, err := os.UserHomeDir()
	if err != nil || session == "" || filepath.Base(session) != session || session == "." || session == ".." {
		return ""
	}
	profile, err := filepath.Abs(copilotHome(home))
	if err != nil {
		return ""
	}
	relative := filepath.Join("session-state", session, "events.jsonl")
	if filepath.Clean(path) != filepath.Join(profile, relative) {
		return ""
	}
	root, err := os.OpenRoot(profile)
	if err != nil {
		return ""
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	file, err := root.Open(relative)
	if err != nil {
		return ""
	}
	defer file.Close()
	const limit = 1 << 20
	offset := max(0, info.Size()-limit)
	data, err := io.ReadAll(io.NewSectionReader(file, offset, info.Size()-offset))
	if err != nil {
		return ""
	}
	if offset > 0 {
		_, data, _ = bytes.Cut(data, []byte("\n"))
	}
	var turn string
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Turn    string            `json:"turnId"`
				Content string            `json:"content"`
				Tools   []json.RawMessage `json:"toolRequests"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		switch event.Type {
		case "assistant.turn_start":
			turn, reply = event.Data.Turn, ""
		case "assistant.message":
			if turn != "" && event.Data.Turn == turn {
				reply = ""
				if len(event.Data.Tools) == 0 {
					reply = event.Data.Content
				}
			}
		}
	}
	return reply
}

func copilotHandoff(key, directory string) string {
	context := watchAgentContextForAgent(key, "copilot", directory)["hookSpecificOutput"].(map[string]any)
	return context["additionalContext"].(string)
}

func copilotStopFeedback(result livewatch.Response, key string, stopActive bool) map[string]any {
	report := makeFeedbackReport(result)
	if report.Complete && !report.NoticesPending || stopActive {
		return map[string]any{}
	}
	reason := fmt.Sprintf("Doomer Quick review needs follow-up before you finish. Run %s watch results --control %s and follow the pending review instructions.", watching.Quote(watchFeedbackExecutable()), key)
	if len(report.Errors) > 0 {
		reason = "Doomer reported incomplete review coverage. Explain this in your final reply: " + report.Errors[0]
	}
	return copilotContinue(reason)
}

func copilotContinue(reason string) map[string]any {
	return map[string]any{"decision": "block", "reason": copilotStopPrefix + reason}
}
