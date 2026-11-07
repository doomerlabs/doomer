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

// A real terminal is required for Codex's interactive frontend. Python's
// standard-library PTY keeps this opt-in Linux test free of extra dependencies.
const terminalDriver = `import os,pty,sys,select,signal,time,fcntl,termios,struct,re
pid,master=pty.fork()
if pid==0:
    os.execvp(sys.argv[1],sys.argv[1:])
fcntl.ioctl(master,termios.TIOCSWINSZ,struct.pack('HHHH',40,120,0,0))
deadline=time.monotonic()+180
seen=b''
trusted=False
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
            if not trusted and b'Hooks need review' in plain:
                trusted=True
                deny=os.getenv('DOOMER_TEST_TRUST_HOOKS')=='deny'
                time.sleep(1)
                os.write(master,b'3' if deny else b'2')
                time.sleep(0.2)
                os.write(master,b'\r')
                os.write(sys.stdout.fileno(),b'\n[Scripted hook trust choice: '+(b'deny' if deny else b'allow')+b']\n')
        if sys.stdin in ready:
            data=os.read(sys.stdin.fileno(),4096)
            if not data: break
            os.write(master,data)
finally:
    try: os.kill(pid,signal.SIGKILL)
    except ProcessLookupError: pass
    os.waitpid(pid,0)
`

var terminalEscapes = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func (j *journey) tui() error {
	started := time.Now()
	prompt := "Create hello.txt containing hello followed by a newline. Follow the installed Doomer skill and finish its review, recording responses to any findings. Keep Codex approval reviewer settings unchanged."
	j.record("Scripted user", prompt)
	j.f.mu.Lock()
	j.f.Findings = true
	j.f.mu.Unlock()
	cmd := exec.CommandContext(j.ctx, "python3", "-c", terminalDriver, j.c.Codex, "--no-alt-screen", "--dangerously-bypass-approvals-and-sandbox", "-C", j.repo, prompt)
	cmd.Dir = j.repo
	choice := "allow"
	if j.c.Scenario == "tui-hooks-denied" {
		choice = "deny"
	}
	j.record("Scripted user", "At the native hook trust screen, choose "+choice+". Keep Codex approval reviewer settings unchanged.")
	cmd.Env = append(j.env, "TERM=xterm-256color", "DOOMER_TEST_TRUST_HOOKS="+choice, "DOOMER_TEST_TERMINAL_LOG="+filepath.Join(j.home, "terminal.log"))
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
	j.result.Turns++
	timer := time.NewTimer(170 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	complete := false
	hookActivated := false
wait:
	for {
		select {
		case <-j.ctx.Done():
			break wait
		case <-timer.C:
			break wait
		case err = <-done:
			input.Close()
			j.record("Command", "Interactive Codex terminal\n"+terminalEscapes.ReplaceAllString(output.String(), ""))
			j.recordTerminalMessages(started)
			return fmt.Errorf("interactive Codex exited before completing the review: %v", err)
		case <-ticker.C:
			tasks, readErr := savedTasks(j.home)
			if readErr != nil {
				continue
			}
			for _, task := range tasks {
				if task.Stage == "complete" && task.Review != nil {
					complete = true
					hookActivated = task.HookActivated
					break wait
				}
			}
		}
	}
	input.Close()
	<-done
	j.record("Command", "Interactive Codex terminal\n"+terminalEscapes.ReplaceAllString(output.String(), ""))
	j.recordTerminalMessages(started)
	if !complete {
		return fmt.Errorf("interactive Codex did not finish within the terminal timeout")
	}
	raw, err := os.ReadFile(filepath.Join(j.repo, "hello.txt"))
	if err != nil || strings.TrimSpace(string(raw)) != "hello" {
		return fmt.Errorf("interactive coding result is missing")
	}
	if choice == "allow" {
		j.result.Checks["native_hook_activated"] = hookActivated
	} else {
		j.result.Checks["native_hook_stayed_inactive"] = !hookActivated
	}
	return j.finish()
}

// Only agent_message events are user-facing transcript text. Reasoning and
// response-item payloads are intentionally excluded from the recording.
func (j *journey) recordTerminalMessages(started time.Time) {
	root := filepath.Join(j.home, ".codex", "sessions")
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
