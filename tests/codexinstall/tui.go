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

// A real terminal is required for the interactive agent frontend. Python's
// standard-library PTY keeps this opt-in Linux test free of extra dependencies.
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

func (j *journey) tui() error {
	started := time.Now()
	prompt := "Create hello.txt containing hello followed by a newline. Follow the installed Doomer skill and finish its review, recording responses to any findings. Keep agent approval settings unchanged."
	if j.c.Scenario == "global-npx-discovery" {
		prompt = "Create hello.py that prints hello followed by a newline when run with Python 3."
	}
	j.record("Scripted user", prompt)
	j.f.mu.Lock()
	j.f.Findings = true
	j.f.mu.Unlock()
	args := []string{"-c", terminalDriver, j.c.Codex, "--no-alt-screen", "--dangerously-bypass-approvals-and-sandbox", "-C", j.repo, prompt}
	if j.c.Harness == "claude" {
		args = []string{"-c", terminalDriver, j.c.Codex, "--permission-mode", claudePermissionMode(j.c), "--model", j.c.Model, "--effort", claudeEffort, "--append-system-prompt", "The active harness is Claude Code. Its Doomer agent identifier is claude."}
		if j.c.Scenario == "tui-hooks-denied" {
			args = append(args, "--settings", `{"disableAllHooks":true}`)
		}
		args = append(args, prompt)
	}
	cmd := exec.CommandContext(j.ctx, "python3", args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = j.repo
	choice := "allow"
	if j.c.Scenario == "tui-hooks-denied" {
		choice = "deny"
	}
	j.record("Scripted user", "At the native hook trust screen, choose "+choice+". Keep agent approval settings unchanged.")
	cmd.Env = append(j.env, "TERM=xterm-256color", "DOOMER_TEST_HARNESS="+j.c.Harness, "DOOMER_TEST_TRUST_HOOKS="+choice, "DOOMER_TEST_TERMINAL_LOG="+filepath.Join(j.home, "terminal.log"))
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
			j.record("Command", "Interactive "+agentName(j.c)+" terminal\n"+terminalEscapes.ReplaceAllString(output.String(), ""))
			j.recordTerminalMessages(started)
			return fmt.Errorf("interactive %s exited before completing the review: %v", agentName(j.c), err)
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
	if complete {
		select {
		case <-time.After(2 * time.Second):
		case <-j.ctx.Done():
		}
	}
	input.Close()
	<-done
	j.record("Command", "Interactive "+agentName(j.c)+" terminal\n"+terminalEscapes.ReplaceAllString(output.String(), ""))
	j.recordTerminalMessages(started)
	if !complete {
		return fmt.Errorf("interactive %s did not finish within the terminal timeout", agentName(j.c))
	}
	var raw []byte
	if j.c.Scenario == "global-npx-discovery" {
		command := exec.CommandContext(j.ctx, "python3", filepath.Join(j.repo, "hello.py"))
		command.Env = j.env
		raw, err = command.Output()
	} else {
		raw, err = os.ReadFile(filepath.Join(j.repo, "hello.txt"))
	}
	if err != nil || strings.TrimSpace(string(raw)) != "hello" {
		return fmt.Errorf("interactive coding result is missing")
	}
	if choice == "allow" && j.c.Scenario != "global-npx-discovery" {
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
