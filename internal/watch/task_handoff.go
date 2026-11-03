package watch

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type taskHandoff struct {
	Repository string
	Session    string
	Prompt     string
	BoundTurn  string
	Created    time.Time
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
