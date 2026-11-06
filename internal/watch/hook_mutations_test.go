package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestShellMutationDetection(t *testing.T) {
	for _, test := range []struct {
		name, command string
		want          bool
	}{
		{"reported search", `rg -n 'could not confirm customer access|Check the access service' doomerlabs | head`, false},
		{"unquoted suffix", `rg confirm customer`, false},
		{"quoted edit commands", `printf '%s\n' 'rm file; git reset; sed -i; tee file; > file'`, false},
		{"double quoted edit commands", `grep "cp file; mv file; apply_patch" file`, false},
		{"comments", "git status # rm file\n# > file", false},
		{"heredoc data", "cat <<'EOF'\nrm file\nwriteFile('file', 'text')\nEOF", false},
		{"here string", `cat <<< 'rm file > file'`, false},
		{"input redirection", `cat < file`, false},
		{"descriptor duplication", `git status 2>&1`, false},
		{"quoted substitution", `echo '$(rm file)'`, false},
		{"filename", `cat /tmp/rm file`, false},
		{"unknown build", `npm run build`, false},
		{"git option text", `git -c alias.example='rm file' status`, false},
		{"sed expression text", `sed 's/rm file/cp file/' file`, false},
		{"script string", `python3 -c 'print("Path(p).write_text(x)")'`, false},
		{"script comment", "python3 <<'PY'\n# Path(p).write_text(x)\nprint('confirm customer')\nPY", false},
		{"malformed command", `rg 'rm file`, false},
		{"dynamic executable", `rm"${suffix}" file`, false},
		{"remove", `rm file`, true},
		{"move", `mv old new`, true},
		{"copy", `cp old new`, true},
		{"quoted executable", `'/bin/rm' file`, true},
		{"tab separator", "rm\tfile", true},
		{"newline separator", "git status\nrm file", true},
		{"chain", `git status && rm file`, true},
		{"pipeline", `cat file | tee other`, true},
		{"subshell", `(rm file)`, true},
		{"conditional", `if test -e file; then rm file; fi`, true},
		{"command substitution", `echo "$(rm file)"`, true},
		{"process substitution", `cat <(rm file)`, true},
		{"output redirection", `printf text>file`, true},
		{"append redirection", `printf text >>file`, true},
		{"combined redirection", `git status &>file`, true},
		{"heredoc output", "cat >file <<'EOF'\ntext\nEOF", true},
		{"patch", "apply_patch <<'PATCH'\npatch content\nPATCH", true},
		{"git options", `git -C repo -c core.autocrlf=false restore file`, true},
		{"go tidy", `go mod tidy`, true},
		{"go get", `go get example.org/pkg`, true},
		{"sed inline", `sed -i.bak 's/a/b/' file`, true},
		{"sed script first", `sed -e 's/a/b/' -i file`, true},
		{"perl inline", `perl -pi -e 's/a/b/' file`, true},
		{"env wrapper", `env MODE=test rm file`, true},
		{"sudo wrapper", `sudo -u root rm file`, true},
		{"shell wrapper", `bash -lc 'rm file'`, true},
		{"shell wrapper search", `bash -lc 'rg "rm file" file'`, false},
		{"python inline", `python3 -c 'Path("file").write_text("text")'`, true},
		{"python heredoc", "python3 <<'PY'\nPath('file').write_bytes(b'text')\nPY", true},
		{"expanded python heredoc", "python3 <<PY\nPath('$FILE').write_text('text')\nPY", true},
		{"shell heredoc", "sh <<'SH'\nrm file\nSH", true},
		{"javascript inline", `node -e 'fs.writeFileSync("file", "text")'`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, key := range []string{"command", "cmd"} {
				input, _ := json.Marshal(map[string]string{key: test.command})
				if got := mutates(HookInput{Tool: "Bash", Input: input}); got != test.want {
					t.Errorf("%s: mutates(%q) = %v, want %v", key, test.command, got, test.want)
				}
			}
		})
	}
}

func TestScriptMutationDetection(t *testing.T) {
	for _, test := range []struct {
		code string
		want bool
	}{
		{`fs.writeFileSync("file", "text")`, true},
		{`Path('file').write_text ('text')`, true},
		{`text(await tools.apply_patch("*** Begin Patch\n*** End Patch"))`, true},
		{`console.log("fs.writeFileSync('file', 'text')")`, false},
		{"// fs.writeFileSync('file', 'text')\nconsole.log('ok')", false},
		{`const confirm = "customer"`, false},
	} {
		input, _ := json.Marshal(map[string]string{"code": test.code})
		if got := mutates(HookInput{Tool: "js", Input: input}); got != test.want {
			t.Errorf("mutates(%q) = %v, want %v", test.code, got, test.want)
		}
	}
}

func TestReadOnlySearchOutsideRepositoryDoesNotReportCaptureFailure(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	input := HookInput{Session: "chat", Turn: "turn", CWD: outside, Event: "UserPromptSubmit", Prompt: "diagnose the retry"}
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		input.Event = event
		if event != "UserPromptSubmit" {
			input.Tool, input.ToolID = "Bash", "read"
			input.Input = json.RawMessage(`{"command":"rg -n 'could not confirm customer access|Check the access service' doomerlabs | head"}`)
		}
		raw, _ := json.Marshal(input)
		var out bytes.Buffer
		hookWithRepositories(context.Background(), root, Config{}, bytes.NewReader(raw), &out, func(context.Context) bool { return false }, true, "codex")
		if got := out.String(); got != "{}\n" {
			t.Fatalf("%s produced review output: %s", event, got)
		}
	}
	var turn Turn
	if err := Read(TurnPath(root, input.Session), &turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.Delivered) != 0 {
		t.Fatalf("read-only search recorded review failure: %v", turn.Delivered)
	}
	if entries, _ := os.ReadDir(root + "/repositories"); len(entries) != 0 {
		t.Fatal("read-only search created review state")
	}
}
