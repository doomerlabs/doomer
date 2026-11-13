package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const terminalDriver = `import os,pty,sys,select,signal,time,fcntl,termios,struct,re
pid,master=pty.fork()
if pid==0:
    os.execvp(sys.argv[1],sys.argv[1:])
fcntl.ioctl(master,termios.TIOCSWINSZ,struct.pack('HHHH',40,120,0,0))
deadline=time.monotonic()+180
seen=b''
trusted=False
claude_trusted=False
api_key_accepted=False
bypass_accepted=False
try:
    while time.monotonic()<deadline:
        ready,_,_=select.select([master,sys.stdin],[],[],0.2)
        if master in ready:
            try: data=os.read(master,65536)
            except OSError: break
            if not data: break
            os.write(sys.stdout.fileno(),data)
            with open(os.environ['DOOMER_TEST_TERMINAL_LOG'],'ab') as log: log.write(data)
            if b'\x1b[6n' in data: os.write(master,b'\x1b[1;1R')
            seen=(seen+data)[-30000:]
            plain=re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]',b'',seen)
            if os.getenv('DOOMER_TEST_HARNESS')=='claude':
                compact=re.sub(rb'\s',b'',plain).lower()
                if not claude_trusted and (b'doyoutrust' in compact or b'isthisaproject' in compact):
                    claude_trusted=True
                    time.sleep(0.3)
                    os.write(master,b'\x1b[B\r')
                if not api_key_accepted and b'usethisapikey' in compact:
                    api_key_accepted=True
                    time.sleep(0.3)
                    os.write(master,b'\r')
                if not bypass_accepted and b'bypasspermissionsmode' in compact and b'yes' in compact:
                    bypass_accepted=True
                    time.sleep(0.3)
                    os.write(master,b'\x1b[B\r')
            if not trusted and b'Hooks need review' in plain:
                trusted=True
                deny=os.getenv('DOOMER_TEST_TRUST_HOOKS')=='deny'
                time.sleep(1)
                os.write(master,b'3' if deny else b'2')
                time.sleep(0.2)
                os.write(master,b'\r')
                os.write(sys.stdout.fileno(),b'\n[Scripted hook trust choice: '+(b'deny' if deny else b'allow')+b']\n')
                if os.getenv('DOOMER_TEST_HOOK_WARNING_ONLY')=='1':
                    time.sleep(0.5)
                    break
        if sys.stdin in ready:
            data=os.read(sys.stdin.fileno(),4096)
            if not data: break
            os.write(master,data)
finally:
    try: os.killpg(pid,signal.SIGKILL)
    except ProcessLookupError: pass
    os.waitpid(pid,0)
`

var terminalEscapes = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// Native trust is exercised in a real terminal. Completion is taken from the
// agent session event, never from a legacy managed-task state file.
func (j *journey) terminalCoding(prompt string, readOnly bool) error {
	started := time.Now()
	j.f.mu.Lock()
	priorReviews, priorFeedback := len(j.f.Reviews), len(j.f.Feedback)
	wantsFeedback := j.f.Findings
	j.f.mu.Unlock()
	j.record("Scripted user", prompt)
	args := []string{"-c", terminalDriver, j.c.Codex, "--no-alt-screen", "--dangerously-bypass-approvals-and-sandbox", "-C", j.repo, prompt}
	if j.c.Harness == "claude" {
		args = []string{"-c", terminalDriver, j.c.Codex, "--permission-mode", claudePermissionMode(j.c), "--model", j.c.Model, "--effort", claudeEffort, prompt}
	}
	cmd := exec.CommandContext(j.ctx, "python3", args...)
	// A watch process can outlive the terminal and retain its output pipe.
	// Bound the copy wait so a failed hook never hangs the whole CI job.
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = j.repo
	cmd.Env = append(j.env, "TERM=xterm-256color", "DOOMER_TEST_HARNESS="+j.c.Harness, "DOOMER_TEST_TRUST_HOOKS=allow", "DOOMER_TEST_TERMINAL_LOG="+filepath.Join(j.home, "terminal.log"))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		input.Close()
		cmd.Process.Kill()
		<-done
		j.record("Terminal", terminalEscapes.ReplaceAllString(output.String(), ""))
		j.recordTerminalMessages(started)
	}()
	j.result.Turns++
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-j.ctx.Done():
			return j.ctx.Err()
		case <-ticker.C:
			if terminalCompleted(j.home, j.c, started) {
				j.f.mu.Lock()
				delivered := len(j.f.Reviews) > priorReviews && (!wantsFeedback || len(j.f.Feedback) > priorFeedback)
				j.f.mu.Unlock()
				if readOnly || j.c.Scenario == "decline-uploads" || delivered {
					return nil
				}
			}
		case err = <-done:
			// Leave the deferred cleanup a completed channel to consume.
			done <- err
			return fmt.Errorf("interactive agent exited before completion: %v", err)
		}
	}
}

func terminalCompleted(home string, c config, since time.Time) bool {
	root := filepath.Join(home, ".codex/sessions")
	if c.Harness == "claude" {
		root = filepath.Join(home, ".claude/projects")
	}
	complete := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		_, err = d.Info()
		if err != nil {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 65536), 4<<20)
		for scanner.Scan() {
			var event struct {
				Type      string `json:"type"`
				Timestamp string `json:"timestamp"`
				Payload   struct {
					Type string `json:"type"`
				} `json:"payload"`
				Message struct {
					StopReason string `json:"stop_reason"`
				} `json:"message"`
			}
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, event.Timestamp)
			if err != nil || at.Before(since) {
				continue
			}
			if event.Type == "event_msg" && (event.Payload.Type == "task_complete" || event.Payload.Type == "turn_complete") {
				complete = true
			}
			if c.Harness == "claude" && event.Type == "assistant" && event.Message.StopReason == "end_turn" {
				complete = true
			}
		}
		return nil
	})
	return complete
}
func (j *journey) recordTerminalMessages(started time.Time) {
	root := filepath.Join(j.home, ".codex", "sessions")
	if j.c.Harness == "claude" {
		root = filepath.Join(j.home, ".claude", "projects")
	}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(started) {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		if j.c.Harness == "claude" {
			_, _ = readClaudeTurn(file, j.record)
			return nil
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 65536), 4<<20)
		for scanner.Scan() {
			var event struct {
				Type    string `json:"type"`
				Payload struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"payload"`
			}
			if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "event_msg" && event.Payload.Type == "agent_message" && event.Payload.Message != "" {
				j.record("Codex", event.Payload.Message)
			}
		}
		return nil
	})
}
