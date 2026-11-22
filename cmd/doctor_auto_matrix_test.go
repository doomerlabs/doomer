package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

func TestDoctorAutoFixSetupMatrix(t *testing.T) {
	isolate(t)
	root := newWatchTestRepo(t)
	var configPath, diagnosis, permissionPath, skillPath string
	var beforeHooks, beforePermissions []byte
	var missingSkill bool
	const receipt = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var packet struct {
			Action    string
			Diagnosis []string
			Result    struct{ Checks []string }
		}
		if err := json.NewDecoder(r.Body).Decode(&packet); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if packet.Action != "auto-start" {
			_ = json.NewEncoder(w).Encode(map[string]bool{"received": true})
			return
		}
		for path, want := range map[string][]byte{configPath: beforeHooks, permissionPath: beforePermissions} {
			data, _ := os.ReadFile(path)
			if string(data) != string(want) {
				t.Errorf("changed %s before reporting diagnosis", filepath.Base(path))
			}
		}
		if missingSkill {
			if _, err := os.Lstat(skillPath); !os.IsNotExist(err) {
				t.Errorf("skill changed before diagnosis: %v", err)
			}
		}
		repair := "none"
		for _, agent := range []string{"claude", "codex"} {
			if slices.Contains(packet.Result.Checks, agent+"_hooks_missing") {
				repair = "install_" + agent + "_hooks"
			}
		}
		if !slices.Contains(packet.Diagnosis, diagnosis) {
			t.Errorf("diagnosis missing %s: %v", diagnosis, packet.Diagnosis)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"code": receipt, "repair": repair})
	}))
	defer server.Close()
	api := server.URL + "/api"
	write := func(t *testing.T, path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	marshal := func(t *testing.T, v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	rules, autoRules, err := npmClaudeRules([]string{mustExecutable()})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex"} {
		events := slices.DeleteFunc(slices.Clone(npmHookEvents), func(event string) bool { return npmEventTimeout(agent, event) == 0 })
		states := []string{"complete", "removed"}
		if agent == "claude" {
			states = append(states, "never-approved", "auto-mode-removed")
			for index := range rules {
				states = append(states, fmt.Sprintf("missing-rule-%d", index))
			}
		} else {
			states = append(states, "revoked", "declined", "edited", "never-approved")
		}
		for mask := 0; mask < 1<<len(events); mask++ {
			for _, permissions := range states {
				protected := agent == "codex" && permissions != "complete" && permissions != "removed"
				if protected && mask != 0 {
					continue
				}
				for _, removeSkill := range []bool{false, true} {
					if agent == "codex" && removeSkill {
						continue
					}
					t.Run(fmt.Sprintf("%s/hooks_%0*b/%s/skill_missing_%t", agent, len(events), mask, permissions, removeSkill), func(t *testing.T) {
						home, profile, manager := t.TempDir(), t.TempDir(), t.TempDir()
						t.Setenv("HOME", home)
						t.Setenv("CLAUDE_CONFIG_DIR", profile)
						t.Setenv("CODEX_HOME", profile)
						credentials := t.TempDir()
						t.Setenv("DOOMER_CONFIG_DIR", credentials)
						t.Setenv("DOOMER_PROFILE", "default")
						configPath = watching.HookFile(home, agent)
						permissionPath = configPath
						command, err := npmHookCommand(npmSetupOptions{home: manager, executable: mustExecutable()}, agent, "--owner=doomer-node")
						if err != nil {
							t.Fatal(err)
						}
						const unrelated = "echo unrelated"
						unrelatedStop := []any{npmJSONObject{"hooks": []any{npmJSONObject{"type": "command", "command": unrelated}}}}
						hooks := npmJSONObject{"Stop": unrelatedStop}
						mergeNPMHooks(hooks, agent, "--owner=doomer-node", command)
						expectedHooks := marshal(t, hooks)
						for i, event := range events {
							if mask&(1<<i) == 0 {
								continue
							}
							if event == "Stop" {
								hooks[event] = unrelatedStop
							} else {
								delete(hooks, event)
							}
						}
						settings := npmJSONObject{"hooks": hooks}
						shared := filepath.Join(home, ".agents", "skills", "doomer")
						skillPath = filepath.Join(profile, "skills", "doomer")
						if err := os.MkdirAll(shared, 0700); err != nil {
							t.Fatal(err)
						}
						write(t, filepath.Join(shared, "SKILL.md"), []byte("---\nname: doomer\n---\n"))
						missingSkill = removeSkill
						if !removeSkill && agent == "claude" {
							if err := os.MkdirAll(filepath.Dir(skillPath), 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.Symlink(shared, skillPath); err != nil {
								t.Fatal(err)
							}
						}
						token := "doomer_anon_" + strings.Repeat("a", 43)
						state := map[string]any{"version": 1, "agents": []string{agent}, "api": api, "executable": mustExecutable(), "hookFiles": []string{configPath},
							"skillPaths": []string{shared, skillPath}, "identities": map[string]string{api: token},
							"grants": []agentUploadGrant{{Scope: "global", API: api, ApprovedAt: time.Now()}},
						}
						diagnosis = "setup_agent_permissions_recorded"
						wantPolicy := ""
						if agent == "claude" {
							bash, auto := slices.Clone(rules), slices.Clone(autoRules)
							switch permissions {
							case "never-approved":
								bash, auto = nil, nil
								diagnosis = "setup_agent_permissions_no_personal_rules"
							case "removed":
								bash, auto = nil, nil
								diagnosis = "setup_agent_permissions_missing"
							case "auto-mode-removed":
								auto = nil
								diagnosis = "setup_agent_permissions_missing"
							default:
								if index, err := strconv.Atoi(strings.TrimPrefix(permissions, "missing-rule-")); err == nil {
									bash = slices.Delete(bash, index, index+1)
									diagnosis = "setup_agent_permissions_incomplete"
								}
							}
							if permissions == "complete" || permissions == "removed" || permissions == "auto-mode-removed" {
								state["claudeReviewPermission"] = npmClaudePermission{Decision: "allow", File: configPath}
							}
							settings["permissions"] = npmJSONObject{"allow": append([]string{"Read"}, bash...)}
							settings["autoMode"] = npmJSONObject{"allow": append([]string{"$defaults"}, auto...)}
						} else {
							evidence := managedConsentEvidence{Account: "anonymous:" + watching.Key(token), Service: api, Profile: "default", CredentialDirectory: credentials, Agent: "codex", AllRepositories: true}
							proposal := proposedCodexReviewPolicy(evidence)
							wantPolicy = proposal.ExtraPolicy
							permissionPath = filepath.Join(profile, "config.toml")
							proposal.ConfigPath = permissionPath
							state["codexPolicies"] = []nodeCodexPolicy{{ConfigPath: permissionPath, ExtraPolicy: wantPolicy}}
							data := []byte("# keep comment\nmodel = 'keep-model'\n")
							if permissions == "complete" {
								var err error
								data, err = patchCodexExtraPolicy(data, wantPolicy)
								if err != nil {
									t.Fatal(err)
								}
							} else {
								diagnosis = "setup_agent_permissions_missing"
							}
							switch permissions {
							case "revoked":
								delete(state, "grants")
							case "declined":
								if err := declineCodexReviewPolicy(managed.Store{Root: filepath.Join(credentials, "managed")}, proposal); err != nil {
									t.Fatal(err)
								}
							case "edited":
								data, err = patchCodexExtraPolicy(data, strings.Replace(wantPolicy, "Respect revoked consent", "Keep this edited policy", 1))
								if err != nil {
									t.Fatal(err)
								}
							case "never-approved":
								delete(state, "codexPolicies")
								diagnosis = "setup_agent_permissions_unknown"
							}
							write(t, permissionPath, data)
						}
						write(t, configPath, marshal(t, settings))
						stateFile := filepath.Join(manager, "state.json")
						write(t, stateFile, marshal(t, state))
						args := []string{"doctor", "--auto-fix", "--repo", root, "--manager-home", manager, "--agent", agent, "--browser-origin", server.URL, "--allow-unconfigured-browser-origin", "--browser-session", "quiet_river_otter"}
						if agent == "claude" {
							args = append(args, "--allow-claude-review-commands")
						}
						run := func(wantError bool) string {
							var err error
							beforeHooks, err = os.ReadFile(configPath)
							if err != nil {
								t.Fatal(err)
							}
							beforePermissions, err = os.ReadFile(permissionPath)
							if err != nil {
								t.Fatal(err)
							}
							out, err := execute(t, "", args...)
							if (err != nil) != wantError {
								t.Fatalf("auto-fix: %v\n%s", err, out)
							}
							return out
						}
						out := run(protected && permissions != "never-approved")
						if protected {
							after, err := os.ReadFile(permissionPath)
							if err != nil || string(after) != string(beforePermissions) || strings.Contains(out, "restored and verified the scoped Doomer review rules") {
								t.Fatalf("Doctor changed a protected policy: %v\n%s", err, out)
							}
							return
						}
						if (permissions != "complete") != strings.Contains(out, "restored and verified the scoped Doomer review rules") {
							t.Fatalf("incorrect permission repair status: %s", out)
						}
						after, _, err := readDoctorAutoHookSettings(configPath)
						if err != nil {
							t.Fatal(err)
						}
						repaired := marshal(t, after["hooks"])
						if string(repaired) != string(expectedHooks) {
							t.Fatalf("unexpected hooks:\n%s\nwant:\n%s", repaired, expectedHooks)
						}
						if agent == "claude" {
							if !doctorReadableSkill(skillPath) {
								t.Fatal("skill link was not restored")
							}
							if rules := after["permissions"].(npmJSONObject)["allow"].([]any); !slices.Contains(rules, any("Read")) {
								t.Fatal("unrelated permission removed")
							}
						} else {
							data, err := os.ReadFile(permissionPath)
							if err != nil {
								t.Fatal(err)
							}
							policy, err := codexExtraPolicy(data)
							if err != nil || policy != wantPolicy || !strings.Contains(string(data), "# keep comment") {
								t.Fatal("Codex policy or unrelated settings were not preserved")
							}
						}
						diagnosis, missingSkill = "setup_agent_permissions_recorded", false
						if out := run(false); !strings.Contains(out, "Doctor made no local changes.") {
							t.Fatalf("second run changed setup: %s", out)
						}
					})
				}
			}
		}
	}
}
