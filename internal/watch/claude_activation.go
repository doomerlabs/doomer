package watch

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

const claudeActivationTTL = 5 * time.Minute

type claudeActivation struct {
	Repository    string
	Session       string
	Task          string
	Created       time.Time
	StartingOwner Owner
	Owner         Owner
	Bootstrap     bool
	PreSeen       bool
	PostSeen      bool
}

func claudeActivationPath(root, repo, session string) string {
	return filepath.Join(root, "onboarding", Key(repo+"\x00"+session)+".json")
}

// An activation is short-lived and confined to this explicitly supplied task.
// Only real hooks can acknowledge it; the onboarding command never simulates hooks.
func BeginClaudeActivation(root, repo, session, task string) error {
	if session == "" || len(session) > 256 || len([]rune(task)) > 3000 {
		return errors.New("invalid Claude activation")
	}
	var turn Turn
	_ = Read(TurnPath(root, session), &turn)
	return Write(claudeActivationPath(root, repo, session), claudeActivation{
		Repository: repo, Session: session, Task: strings.TrimSpace(task), Created: time.Now(), StartingOwner: turn.Owner,
		Bootstrap: turn.Owner.Session == "" || turn.Agent != "claude" || strings.TrimSpace(task) != "" && strings.TrimSpace(task) != strings.TrimSpace(turn.Prompt),
	})
}

func freshClaudeActivation(a claudeActivation, repo, session string) bool {
	age := time.Since(a.Created)
	return a.Repository == repo && a.Session == session && !a.Created.IsZero() && age >= 0 && age < claudeActivationTTL
}

// Attach hooks installed mid-turn to the task supplied during onboarding. A
// later UserPromptSubmit replaces this task before it can attach stale context.
func activateClaudeHook(root, repo string, input HookInput, turn *Turn, owner *Owner) {
	if input.ToolID == "" || input.Tool == "" || input.Event != "PreToolUse" && input.Event != "PostToolUse" {
		return
	}
	file := claudeActivationPath(root, repo, input.Session)
	_ = Locked(root, "claude-activation-"+Key(repo+"\x00"+input.Session), func() error {
		var a claudeActivation
		if Read(file, &a) != nil || !freshClaudeActivation(a, repo, input.Session) {
			return nil
		}
		if a.Bootstrap {
			if a.Task == "" {
				return nil
			}
			err := Locked(root, input.Session, func() error {
				var current Turn
				_ = Read(TurnPath(root, input.Session), &current)
				if current.Owner != a.StartingOwner {
					return errors.New("Claude turn changed during onboarding")
				}
				if input.Turn == "" {
					owner.Turn = "onboard-" + Key(a.Created.String())
				}
				current = Turn{Owner: *owner, Prompt: a.Task, Next: 1, Agent: "claude"}
				if err := Write(TurnPath(root, input.Session), current); err != nil {
					return err
				}
				*turn = current
				return nil
			})
			if err != nil {
				return err
			}
			a.Bootstrap = false
			a.Owner = *owner
		}
		if turn.Owner != *owner || turn.Agent != "claude" || strings.TrimSpace(turn.Prompt) == "" {
			return nil
		}
		if a.Owner.Session != "" && a.Owner != *owner {
			return nil
		}
		a.Owner = *owner
		if input.Event == "PreToolUse" {
			a.PreSeen = true
		} else {
			a.PostSeen = true
		}
		return Write(file, a)
	})
}

// A new prompt owns its own task; never inject the old onboarding request into it.
func advanceClaudeActivation(root, repo string, owner Owner) {
	file := claudeActivationPath(root, repo, owner.Session)
	_ = Locked(root, "claude-activation-"+Key(repo+"\x00"+owner.Session), func() error {
		var a claudeActivation
		if Read(file, &a) != nil || !freshClaudeActivation(a, repo, owner.Session) {
			return nil
		}
		a.Task = ""
		a.Bootstrap = false
		a.Owner = owner
		a.StartingOwner = owner
		a.PreSeen = false
		a.PostSeen = false
		return Write(file, a)
	})
}

// ClaudeActivationStatus reports observed runtime events, never inferred trust.
func ClaudeActivationStatus(root, repo, session string) string {
	var a claudeActivation
	if Read(claudeActivationPath(root, repo, session), &a) != nil || !freshClaudeActivation(a, repo, session) {
		return "unconfirmed"
	}
	if a.Bootstrap || !a.PreSeen || !a.PostSeen {
		return "waiting_for_hooks"
	}
	var turn Turn
	if Read(TurnPath(root, session), &turn) != nil || turn.Owner != a.Owner || turn.Agent != "claude" || strings.TrimSpace(turn.Prompt) == "" {
		return "unconfirmed"
	}
	return "ready"
}
