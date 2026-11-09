package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
)

// Drive the browser decision while the real coding agent is still running.
// Browser-only completion is preferred. If host review explicitly rejects the
// persistent policy, verify the one-time chat approval recovery and fresh reuse.
func (j *journey) browserOnboarding() error {
	if j.c.Harness != "codex" {
		return fmt.Errorf("browser onboarding currently targets Codex")
	}
	updater := selfupdate.Updater{Home: j.home, Versioned: true}
	if _, err := updater.Apply(j.ctx, j.c.CandidateVersion, false); err != nil {
		return err
	}
	if err := installCandidate(j.c, j.home); err != nil {
		return err
	}
	j.env = append(j.env, "CODEX_INTERNAL_ORIGINATOR_OVERRIDE=Codex Desktop", "CODEX_SQLITE_HOME="+filepath.Join(j.home, ".codex"))
	binary := runtimeversion.Binary(j.home, j.c.CandidateVersion)
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(j.ctx, binary, args...)
		cmd.Dir = j.repo
		cmd.Env = j.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("fixture setup: %w: %s", err, out)
		}
		return out, nil
	}
	if _, err := run("install", "--repo", j.repo); err != nil {
		return err
	}
	if j.c.SkillsBootstrap != "" || len(j.c.RepoBootstraps) > 0 {
		if err := installCandidateSkills(j.c, j.home, j.repo); err != nil {
			return err
		}
	}
	if err := installCandidateDiscovery(j.repo); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(j.repo, "admin.ts"), []byte("export const adminLinks = ['Overview', 'Waitlist', 'Users'];\n"), 0600); err != nil {
		return err
	}
	if err := j.git(j.repo, "add", "."); err != nil {
		return err
	}
	if err := j.git(j.repo, "-c", "commit.gpgsign=false", "commit", "-qm", "Existing team skill; fresh developer setup"); err != nil {
		return err
	}
	// Software consent is a different decision; this scenario isolates upload
	// consent, browser waiting, policy installation, and reuse in a fresh chat.

	configPath := filepath.Join(j.home, ".codex/config.toml")
	var mu sync.Mutex
	var linkShown, waitStarted, browserApproved, cleanBeforeApproval bool
	var once sync.Once
	done := make(chan struct{})
	var browser sync.WaitGroup
	defer func() { close(done); browser.Wait() }()
	record := func(kind, text string) {
		j.record(kind, text)
		mu.Lock()
		if kind == "Codex" && strings.Contains(text, "/cli/setup#request=") {
			linkShown = true
		}
		trigger := kind == "Command started" && strings.Contains(text, "--wait") && strings.Contains(text, "login") && linkShown
		if trigger {
			waitStarted = true
		}
		mu.Unlock()
		if trigger {
			once.Do(func() {
				browser.Add(1)
				go func() {
					defer browser.Done()
					select {
					case <-time.After(2200 * time.Millisecond):
					case <-done:
						return
					case <-j.ctx.Done():
						return
					}
					data, _ := os.ReadFile(configPath)
					j.f.mu.Lock()
					clean := j.f.Uploads == 0 && !strings.Contains(string(data), "Doomer repository review authorization")
					j.f.Approved = true
					j.f.mu.Unlock()
					mu.Lock()
					browserApproved = true
					cleanBeforeApproval = clean
					mu.Unlock()
					j.record("Browser decision", "User chose Allow on the displayed setup page while the wait command was running.")
				}()
			})
		}
	}
	j.c.ReviewApprovals = true
	j.result.Turns++
	onboarding, err := runAgent(j.ctx, j.c, j.repo, j.env, "Set up Doomer review in this repository using its existing installation. I allow automatic Doomer software updates and their version metadata reporting. Ask for repository upload approval on its browser page. Do not install hooks; I will use explicit reviews in new coding chats. This task is setup only; do not change or review source.", "", record)
	if err != nil {
		return err
	}
	mu.Lock()
	j.result.Checks["approval_link_presented"] = linkShown
	j.result.Checks["agent_waited_for_browser"] = waitStarted && browserApproved
	j.result.Checks["no_policy_or_upload_before_approval"] = cleanBeforeApproval
	mu.Unlock()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	installed := strings.Contains(string(raw), "Doomer repository review authorization")
	if !installed {
		// A host can require direct chat authorization for persistent settings;
		// never bypass its refusal or manufacture a browser-only success.
		messages := strings.ToLower(strings.Join(onboarding.Messages, "\n"))
		j.f.mu.Lock()
		noUpload := j.f.Uploads == 0 && len(j.f.Reviews) == 0
		j.f.mu.Unlock()
		if !noUpload || !strings.Contains(messages, "policy") ||
			(!strings.Contains(messages, "approv") && !strings.Contains(messages, "allow")) {
			return fmt.Errorf("browser onboarding stopped without a recoverable policy decision")
		}
		j.record("Host approval recovery", "Browser approval was saved, but the host required direct approval for persistent policy installation.")
		j.result.Turns++
		answer := "I approve installing the displayed scoped Codex reviewer policy for all current and future repositories used by this agent with this Doomer account, service, and profile. Preserve unrelated settings and the earlier no-hooks choice. Finish this same setup task; do not upload or review source in this setup turn."
		j.record("Scripted user", answer)
		if _, err := runAgent(j.ctx, j.c, j.repo, j.env, answer, onboarding.Session, record); err != nil {
			return err
		}
		raw, err = os.ReadFile(configPath)
		if err != nil {
			return err
		}
		installed = strings.Contains(string(raw), "Doomer repository review authorization")
	}
	j.result.Checks["approved_policy_installed"] = installed
	tasks, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	ready := false
	for _, task := range tasks {
		if task.AccountID != "" && !task.OnboardingPending {
			ready = true
		}
	}
	j.result.Checks["onboarding_completed_after_required_approvals"] = ready && installed
	if !j.result.Checks["onboarding_completed_after_required_approvals"] {
		return fmt.Errorf("approved onboarding did not finish policy setup")
	}
	// The next process must reuse the saved policy/grant without any approval
	// response from the fixture. This is the successful customer reproduction.
	j.result.Turns++
	before := len(tasks)
	turn, err := runAgent(j.ctx, j.c, j.repo, j.env, "Remove Waitlist from adminLinks in admin.ts and complete the review.", "", j.record)
	if err != nil {
		return err
	}
	tasks, err = savedTasks(j.home)
	if err != nil {
		return err
	}
	completed := 0
	for _, task := range tasks {
		if task.Stage == "complete" && task.Review != nil {
			completed++
		}
	}
	j.f.mu.Lock()
	j.result.Checks["one_browser_grant"] = j.f.Creates == 1
	j.result.Checks["fresh_review_uploaded"] = len(j.f.Reviews) >= 1
	j.f.mu.Unlock()
	j.result.Checks["fresh_session_completed_without_approval"] = len(tasks) > before && completed >= 1 && !turn.UpdateRequested && !turn.PolicyOffered && !turn.HooksOffered
	j.result.Checks["optional_hooks_not_installed"] = true
	if _, err := os.Stat(filepath.Join(j.home, ".codex/hooks.json")); err == nil {
		j.result.Checks["optional_hooks_not_installed"] = false
	}
	for name, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("browser onboarding regression: %s", name)
		}
	}
	return nil
}
