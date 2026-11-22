package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

func TestDoctorAutoFixRepairsHooksAndSkillIndependently(t *testing.T) {
	isolate(t)
	manager := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	claude := t.TempDir()
	sharedSkill := filepath.Join(home, ".agents", "skills", "doomer")
	profileSkill := filepath.Join(claude, "skills", "doomer")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("DOOMER_API_URL", "")
	file := filepath.Join(claude, "settings.json")
	const before = `{"permissions":{"deny":["Bash(curl *)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo unrelated"}]}]}}`
	if err := os.WriteFile(file, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	type packet struct {
		Action, Code, Outcome, BrowserCode, AutoReceipt string
		Diagnosis                                       []string
		Result                                          struct {
			Checks  []string
			Changes []doctorRepairChange
		}
	}
	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var repair atomic.Value
	repair.Store("install_codex_hooks")
	requests := make(chan packet, 8)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p packet
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil || r.URL.Path != "/api/docs/doctor-session" {
			t.Errorf("unexpected request: %s, %v", r.URL.Path, err)
			w.WriteHeader(400)
			return
		}
		requests <- p
		if p.Action == "auto-start" {
			attempt := attempts.Add(1)
			if attempt == 4 {
				if _, err := os.Lstat(profileSkill); !os.IsNotExist(err) {
					t.Errorf("skill was changed before diagnosis was reported: %v", err)
				}
			}
			if attempt <= 2 {
				if data, err := os.ReadFile(file); err != nil || string(data) != before {
					t.Errorf("hook settings changed before Doctor received the diagnosis: %v", err)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"code": token, "repair": repair.Load().(string)})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]bool{"received": true})
		}
	}))
	defer server.Close()
	state, err := json.Marshal(map[string]any{
		"version": 1, "agents": []string{"claude"}, "api": server.URL + "/api",
		"executable": mustExecutable(), "hookFiles": []string{file}, "grants": []string{},
		"skillPaths": []string{sharedSkill, profileSkill},
	})
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(manager, "state.json")
	if err := os.WriteFile(stateFile, state, 0600); err != nil {
		t.Fatal(err)
	}
	const browserCode = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	args := []string{"doctor", "--auto-fix", "--repo", newWatchTestRepo(t), "--manager-home", manager,
		"--browser-origin", server.URL, "--allow-unconfigured-browser-origin", "--browser-session", browserCode}
	expectChecking := func() {
		t.Helper()
		if got := <-requests; got.Action != "auto-checking" || got.Code != browserCode {
			t.Fatalf("unexpected check start: %+v", got)
		}
	}
	expectBrowserResult := func(check, receipt string, want ...string) []doctorRepairChange {
		t.Helper()
		got := <-requests
		if got.Action != "submit" || got.Code != browserCode || got.AutoReceipt != receipt || len(got.Result.Checks) == 0 || got.Result.Checks[0] != check || (receipt != "" && !slices.Contains(got.Result.Checks, "setup_binary_ready")) {
			t.Fatalf("unexpected browser report: %+v", got)
		}
		for _, code := range want {
			if !slices.Contains(got.Result.Checks, code) {
				t.Fatalf("browser report missing %s: %+v", code, got)
			}
		}
		return got.Result.Changes
	}
	if out, err := execute(t, "", args...); err == nil || !strings.Contains(err.Error(), "unsupported repair") {
		t.Fatalf("cross-agent repair: %s %v", out, err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != before {
		t.Fatalf("rejected repair changed settings: %s %v", got, err)
	}
	expectChecking()
	start := <-requests
	if start.Action != "auto-start" || start.BrowserCode != browserCode || !slices.Equal(start.Result.Checks, []string{"hook_configuration_checked", "claude_hooks_missing"}) {
		t.Fatalf("unexpected diagnostic: %+v", start)
	}
	for _, code := range []string{"claude_hooks_missing", "setup_binary_ready", "setup_skill_unavailable", "setup_agent_permissions_no_personal_rules", "setup_upload_missing"} {
		if !slices.Contains(start.Diagnosis, code) {
			t.Fatalf("diagnosis missing %s: %+v", code, start)
		}
	}
	expectBrowserResult("auto_fix_failed", token)
	repair.Store("install_claude_hooks")
	if out, err := execute(t, "", args...); err != nil || !strings.Contains(out, "repaired and verified") {
		t.Fatalf("expected repaired hooks: %s %v", out, err)
	}
	expectChecking()
	<-requests
	if got := <-requests; got.Action != "auto-finish" || got.Code != token || got.Outcome != "repaired" {
		t.Fatalf("unexpected outcome: %+v", got)
	}
	hookChanges := expectBrowserResult("auto_fix_repaired", token)
	if len(hookChanges) != 1 || hookChanges[0].Path != file || !strings.Contains(hookChanges[0].Diff, "UserPromptSubmit") ||
		strings.Contains(hookChanges[0].Diff, "echo unrelated") {
		t.Fatalf("expected a scoped hook diff: %+v", hookChanges)
	}
	after, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(after), "echo unrelated") || !strings.Contains(string(after), "Bash(curl *)") {
		t.Fatalf("repair lost local settings: %s %v", after, err)
	}
	if got, err := os.ReadFile(stateFile); err != nil || string(got) != string(state) {
		t.Fatalf("repair changed installer consent state: %s %v", got, err)
	}
	repair.Store("none")
	if out, err := execute(t, "", args...); err != nil || !strings.Contains(out, "made no local changes") {
		t.Fatalf("second run was not a no-op: %s %v", out, err)
	}
	expectChecking()
	if start := <-requests; !slices.Contains(start.Result.Checks, "claude_hooks_installed") {
		t.Fatalf("unexpected repeat check: %+v", start)
	}
	if finish := <-requests; finish.Outcome != "unchanged" {
		t.Fatalf("repeat changed local state: %+v", finish)
	}
	expectBrowserResult("auto_fix_unchanged", token)
	if got, err := os.ReadFile(file); err != nil || string(got) != string(after) {
		t.Fatalf("repeat rewrote settings: %s %v", got, err)
	}
	if err := os.MkdirAll(sharedSkill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedSkill, "SKILL.md"), []byte("---\nname: doomer\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, "", args...); err != nil || !strings.Contains(out, "restored and verified the link") || strings.Contains(out, "made no local changes") {
		t.Fatalf("expected skill-only repair: %s %v", out, err)
	}
	expectChecking()
	if start := <-requests; !slices.Contains(start.Diagnosis, "setup_skill_unavailable") || !slices.Contains(start.Diagnosis, "claude_hooks_installed") {
		t.Fatalf("unexpected skill repair diagnosis: %+v", start)
	}
	if finish := <-requests; finish.Outcome != "unchanged" {
		t.Fatalf("skill repair altered hook outcome: %+v", finish)
	}
	skillChanges := expectBrowserResult("auto_fix_unchanged", token, "setup_skill_restored")
	if len(skillChanges) != 1 || skillChanges[0].Path != profileSkill || !strings.Contains(skillChanges[0].Diff, "+"+sharedSkill) {
		t.Fatalf("expected a skill link diff: %+v", skillChanges)
	}
	if link, err := os.Readlink(profileSkill); err != nil || link != sharedSkill || !doctorReadableSkill(profileSkill) {
		t.Fatalf("expected a readable skill link: %s %v", link, err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != string(after) {
		t.Fatalf("skill repair rewrote hook settings: %s %v", got, err)
	}
	config, _, err := readDoctorAutoHookSettings(file)
	if err != nil {
		t.Fatal(err)
	}
	rules, _, err := npmClaudeRules([]string{mustExecutable()})
	if err != nil {
		t.Fatal(err)
	}
	allow := []any{"Bash(echo ok)"}
	for _, rule := range rules[1:] {
		allow = append(allow, rule)
	}
	config["permissions"] = npmJSONObject{"allow": allow}
	beforePermissionsRepair, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, beforePermissionsRepair, 0600); err != nil {
		t.Fatal(err)
	}
	permissionArgs := append(slices.Clone(args), "--agent", "claude", "--allow-claude-review-commands")
	if out, err := execute(t, "", permissionArgs...); err != nil || !strings.Contains(out, "verified the scoped Doomer review rules") || strings.Contains(out, "made no local changes") {
		t.Fatalf("expected permissions-only repair: %s %v", out, err)
	}
	expectChecking()
	if start := <-requests; !slices.Contains(start.Diagnosis, "setup_agent_permissions_incomplete") || !slices.Contains(start.Diagnosis, "claude_hooks_installed") {
		t.Fatalf("unexpected permission repair diagnosis: %+v", start)
	}
	if finish := <-requests; finish.Outcome != "unchanged" {
		t.Fatalf("permission repair altered hook outcome: %+v", finish)
	}
	permissionChanges := expectBrowserResult("auto_fix_unchanged", token, "setup_agent_permissions_repaired")
	if len(permissionChanges) != 1 || permissionChanges[0].Path != file ||
		!strings.Contains(permissionChanges[0].Diff, "watch hypotheses --control *") ||
		strings.Contains(permissionChanges[0].Diff, "Bash(echo ok)") {
		t.Fatalf("expected a scoped permissions diff: %+v", permissionChanges)
	}
	verified, _, err := readDoctorAutoHookSettings(file)
	if err != nil {
		t.Fatal(err)
	}
	gotRules := verified["permissions"].(npmJSONObject)["allow"].([]any)
	if !slices.Contains(gotRules, any(rules[0])) || !slices.Contains(gotRules, any("Bash(echo ok)")) {
		t.Fatalf("missing repaired or existing rules: %v", gotRules)
	}
	var savedPermission npmClaudePermission
	installer, err := readNPMSetupState(manager)
	if err != nil || installer.get("claudeReviewPermission", &savedPermission) != nil || savedPermission.Decision != "allow" {
		t.Fatalf("reviewer choice not recorded: %v", err)
	}
	if err := os.Remove(profileSkill); err != nil {
		t.Fatal(err)
	}
	for _, obstruction := range []string{"dangling link", "local file"} {
		missingTarget := filepath.Join(home, "missing-skill")
		if obstruction == "dangling link" {
			if err := os.Symlink(missingTarget, profileSkill); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(profileSkill, []byte("local file"), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := execute(t, "", args...); err == nil || !strings.Contains(err.Error(), "move the unreadable Doomer skill") ||
			!strings.Contains(out, "Doctor could not restore the Doomer skill link") || strings.Contains(out, "made no local changes") {
			t.Fatalf("expected actionable skill obstruction: %s %v", out, err)
		}
		expectChecking()
		if start := <-requests; !slices.Contains(start.Diagnosis, "setup_skill_unavailable") {
			t.Fatalf("unexpected skill diagnosis: %+v", start)
		}
		<-requests // The hook plan remains unchanged.
		if changes := expectBrowserResult("auto_fix_unchanged", token, "setup_skill_restore_failed"); len(changes) != 0 {
			t.Fatalf("expected no applied changes: %+v", changes)
		}
		if obstruction == "dangling link" {
			if got, err := os.Readlink(profileSkill); err != nil || got != missingTarget {
				t.Fatalf("dangling link was changed: %s %v", got, err)
			}
		} else if got, err := os.ReadFile(profileSkill); err != nil || string(got) != "local file" {
			t.Fatalf("local file was changed: %s %v", got, err)
		}
		if err := os.Remove(profileSkill); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := execute(t, "", append(slices.Clone(args), "--agent", "codex")...); err == nil || !strings.Contains(err.Error(), "select an agent") {
		t.Fatalf("expected unsupported personal setup: %s %v", out, err)
	}
	expectChecking()
	expectBrowserResult("auto_fix_failed", "")
	for _, tc := range []struct{ settings, message, outcome string }{
		{`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"doomer agent-hook claude"}]}]}}`, "existing Doomer hooks need manual attention", "auto_fix_hook_conflict"},
		{`{"disableAllHooks":true}`, "hooks are disabled", "auto_fix_hooks_disabled"},
	} {
		if err := os.WriteFile(file, []byte(tc.settings), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := execute(t, "", args...); err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("expected blocked auto-fix: %s %v", out, err)
		}
		expectChecking()
		expectBrowserResult(tc.outcome, "")
		if got, err := os.ReadFile(file); err != nil || string(got) != tc.settings {
			t.Fatalf("blocked auto-fix changed settings: %s %v", got, err)
		}
	}
}

func TestDoctorAutoFixRestoresMissingScopedDevelopmentHook(t *testing.T) {
	isolate(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	const owner = "--owner=doomer-node-dev"
	const directCommand = "doomer-dev watch agent --agent=claude --owner=doomer-node-dev"
	scopedCommand := "/usr/bin/python3 " + filepath.Join(home, ".doomer-developer", "doomer_hook_scope.py") +
		" development " + filepath.Join(home, ".local", "bin", "doomer-dev") +
		" watch agent --agent=claude " + owner + " --manager-home=" + filepath.Join(home, "dev-manager")
	productionCommand := "'" + filepath.Join(home, ".local", "bin", "doomer") +
		"' --profile 'default' --api-url 'https://doomer.ai/api' agent-hook --agent claude --managed-task-adapter --state 'production.json'"
	group := func(command string) npmJSONObject {
		return npmJSONObject{"hooks": []any{npmJSONObject{"type": "command", "command": command}}}
	}
	for _, tc := range []struct {
		name, wantCommand string
		hooks             npmJSONObject
	}{
		{"some scoped hooks missing", scopedCommand, npmJSONObject{
			"Stop":             []any{group(productionCommand), group(scopedCommand)},
			"UserPromptSubmit": []any{group(productionCommand), group(scopedCommand)},
			"PreToolUse":       []any{group(productionCommand)},
			"PermissionDenied": []any{npmJSONObject{
				"matcher": "Bash", "hooks": []any{npmJSONObject{"type": "command", "command": scopedCommand}},
			}},
		}},
		{"all missing in shared profile", scopedCommand, npmJSONObject{
			"Stop":       []any{group(productionCommand)},
			"PreToolUse": []any{group(productionCommand)},
		}},
		{"all missing in isolated profile", scopedCommand, npmJSONObject{}},
		{"existing direct routing", directCommand, npmJSONObject{
			"Stop": []any{group(directCommand)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "settings.json")
			target := doctorAutoTarget{agent: "claude", file: file, owner: owner, command: directCommand, scopedCommand: scopedCommand}
			before := npmJSONObject{
				"permissions": npmJSONObject{"deny": []any{"Bash(curl *)"}},
				"hooks":       tc.hooks,
			}
			original, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, original, 0600); err != nil {
				t.Fatal(err)
			}
			applied, err := configureNPMHookFile(file, target.agent, owner, directCommand, target.inspectHooks, nil)
			if err != nil || !applied {
				t.Fatalf("expected the missing development hook to be restored: %v", err)
			}
			after, _, err := readDoctorAutoHookSettings(file)
			if err != nil {
				t.Fatal(err)
			}
			hooks, err := npmValidateHooks(after)
			if err != nil {
				t.Fatal(err)
			}
			command, missing, err := target.inspectHooks(after, hooks)
			if err != nil || missing || command != tc.wantCommand {
				t.Fatalf("expected verified scoped hooks: missing=%v, err=%v", missing, err)
			}
			originalHooks := before["hooks"].(npmJSONObject)
			for event, originalGroups := range originalHooks {
				currentGroups := hooks[event].([]any)
				preservedCount := len(originalGroups.([]any))
				if len(currentGroups) < preservedCount {
					t.Fatalf("existing %s hooks were removed", event)
				}
				want, _ := json.Marshal(originalGroups)
				got, _ := json.Marshal(currentGroups[:preservedCount])
				if string(got) != string(want) {
					t.Fatalf("existing %s hooks were modified", event)
				}
			}

			if got := after["permissions"].(npmJSONObject)["deny"].([]any); !slices.Equal(got, []any{"Bash(curl *)"}) {
				t.Fatal("repair changed agent permissions")
			}
			backups, err := filepath.Glob(file + ".before-doomer-node-*")
			if err != nil || len(backups) != 1 {
				t.Fatalf("expected one settings backup: %v %v", backups, err)
			}
			if data, err := os.ReadFile(backups[0]); err != nil || string(data) != string(original) {
				t.Fatalf("backup was not preserved: %v", err)
			}
			if applied, err := configureNPMHookFile(file, target.agent, owner, directCommand, target.inspectHooks, nil); err != nil || applied {
				t.Fatalf("expected subsequent auto-fix to be a no-op: %v", err)
			}
		})
	}
}

func TestDoctorAutoChecksDoNotSendEnvironmentTokenToCheckoutService(t *testing.T) {
	isolate(t)
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	root, manager := t.TempDir(), t.TempDir()
	t.Setenv("DOOMER_TOKEN", "test-inherited-token")
	if err := os.WriteFile(filepath.Join(root, ".doomer.env"), []byte("DOOMER_API_URL="+server.URL+"/api\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager, "state.json"), []byte(`{"version":1,"api":"https://doomer.ai/api"}`), 0600); err != nil {
		t.Fatal(err)
	}
	checks := doctorAutoSetupChecks(&cobra.Command{}, root, manager, "claude")
	if probes.Load() != 0 || !slices.Contains(checks, "setup_auth_unknown") {
		t.Fatalf("expected local-only credential detection, got %v and %d service probes", checks, probes.Load())
	}
}
