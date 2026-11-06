package watch

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// ResumeHandoffPrompt is the command/hook contract for restoring a saved task.
const ResumeHandoffPrompt = "Resume after doomer.ai setup"

type taskHandoff struct {
	Repository       string
	Session          string
	Prompt           string
	ContextDelivered bool
	BoundTurn        string
	Created          time.Time
}

func taskHandoffPath(root, repository, session string) string {
	return filepath.Join(root, "task-handoffs", Key(repository+"\x00"+session)+".json")
}

// SaveTaskHandoff stores only the explicitly supplied task, never a transcript.
func SaveTaskHandoff(root, repository, session string, input io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(input, 16001))
	if err != nil {
		return err
	}
	prompt := strings.TrimSpace(string(raw))
	if session == "" || len(session) > 256 || prompt == "" || len(raw) > 16000 || len([]rune(prompt)) > 3000 {
		return errors.New("task requires a session and 1–3000 characters of original request")
	}
	return Locked(root, "task-handoff-"+Key(repository+"\x00"+session), func() error {
		return Write(taskHandoffPath(root, repository, session), taskHandoff{Repository: repository, Session: session, Prompt: prompt, Created: time.Now()})
	})
}

// pendingTaskPrompt recovers an explicitly saved request when a new runtime
// first sees a tool event after missing UserPromptSubmit. Only a real hook calls
// this helper. Reading it neither creates a receipt nor consumes the handoff.
func pendingTaskPrompt(root, repository string, owner Owner) string {
	var saved taskHandoff
	if owner.Session == "" || owner.Turn == "" || Read(taskHandoffPath(root, repository, owner.Session), &saved) != nil {
		return ""
	}
	age := time.Since(saved.Created)
	if saved.Repository != repository || saved.Session != owner.Session || saved.Created.IsZero() || age < 0 || age > 24*time.Hour ||
		(saved.BoundTurn != "" && saved.BoundTurn != owner.Turn) {
		return ""
	}
	return saved.Prompt
}

// The saved request applies to one editing turn after onboarding. Read-only
// setup and trust-confirmation turns do not consume it. Further turns use their
// own request instead of silently inheriting an old task.
func handoffPrompt(root, repository string, owner Owner, current string) string {
	result := current
	_ = Locked(root, "task-handoff-"+Key(repository+"\x00"+owner.Session), func() error {
		file := taskHandoffPath(root, repository, owner.Session)
		var saved taskHandoff
		if Read(file, &saved) != nil || saved.Repository != repository || saved.Session != owner.Session || saved.Created.IsZero() || time.Since(saved.Created) > 24*time.Hour || time.Since(saved.Created) < 0 || saved.Prompt == "" {
			return nil
		}
		if saved.BoundTurn != "" && saved.BoundTurn != owner.Turn {
			return nil
		}
		if saved.BoundTurn == "" {
			saved.BoundTurn = owner.Turn
			if err := Write(file, saved); err != nil {
				return err
			}
		}
		result = "Original request before Doomer setup:\n" + saved.Prompt
		if strings.TrimSpace(current) != "" && strings.TrimSpace(current) != saved.Prompt {
			continuation := []rune(current)
			if len(continuation) > 600 {
				continuation = continuation[:600]
			}
			result += "\n\nContinuation after setup:\n" + string(continuation)
		}
		return nil
	})
	return result
}

// ResumeTaskContext hands the saved request to one resumed prompt. The original
// remains scoped to the first editing turn for review attribution.
func ResumeTaskContext(root, repository, session string) (string, error) {
	var prompt string
	err := Locked(root, "task-handoff-"+Key(repository+"\x00"+session), func() error {
		file := taskHandoffPath(root, repository, session)
		var saved taskHandoff
		if err := Read(file, &saved); err != nil {
			return nil
		}
		if saved.Repository != repository || saved.Session != session || saved.ContextDelivered || saved.BoundTurn != "" || saved.Created.IsZero() || time.Since(saved.Created) < 0 || time.Since(saved.Created) > 24*time.Hour {
			return nil
		}
		saved.ContextDelivered = true
		if err := Write(file, saved); err != nil {
			return err
		}
		prompt = saved.Prompt
		return nil
	})
	return prompt, err
}
