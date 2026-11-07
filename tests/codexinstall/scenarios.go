package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/doomerlabs/doomer/internal/managed"
)

var scenarioNames = []string{"install", "first-review", "new-chat", "worktrees", "decline-updates", "decline-uploads", "interrupted", "upgrade", "upgrade-defer", "service-failure", "repeat-onboarding", "read-only", "personal", "upgrade-untracked", "upgrade-personal", "tui", "hooks-off", "revoked-uploads", "interrupted-review", "tui-hooks-denied", "portable-invalid", "upgrade-custom", "deep-review", "portable-restart"}

func validScenario(name string) bool {
	for _, candidate := range scenarioNames {
		if name == candidate {
			return true
		}
	}
	return false
}

type journey struct {
	c           config
	ctx         context.Context
	home, repo  string
	env         []string
	f           *fixture
	result      *report
	codexConfig string
	record      func(string, string)
	mu          sync.Mutex
}

func runAgent(ctx context.Context, c config, repo string, env []string, prompt, session string, record func(string, string)) (codexTurn, error) {
	if c.Harness == "claude" {
		return runClaude(ctx, c, repo, env, prompt, session, record)
	}
	record("Scripted user", prompt)
	args := []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox"}
	if session != "" {
		args = append(args, "resume", session)
	} else {
		args = append(args, "-C", repo)
	}
	cmd := exec.CommandContext(ctx, c.Codex, append(args, "-")...)
	cmd.Dir = repo
	cmd.Env = env
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return codexTurn{}, err
	}
	if err = cmd.Start(); err != nil {
		return codexTurn{}, err
	}
	observed, readErr := readCodexTurn(out, record)
	waitErr := cmd.Wait()
	if readErr != nil {
		return observed, readErr
	}
	if observed.Failed || waitErr != nil {
		return observed, fmt.Errorf("Codex execution failed: %s", strings.TrimSpace(stderr.String()))
	}
	return observed, nil
}

func (j *journey) coding(repo, prompt string) error {
	prior, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, task := range prior {
		seen[task.Handle] = true
	}
	j.mu.Lock()
	j.result.Turns++
	j.mu.Unlock()
	observed, err := runAgent(j.ctx, j.c, repo, j.env, prompt, "", j.record)
	if err != nil {
		return err
	}
	if observed.PolicyOffered && j.c.Scenario != "portable-restart" {
		return fmt.Errorf("unexpected repeated Codex settings proposal")
	}
	if observed.UpdateRequested {
		return fmt.Errorf("unexpected repeat update approval")
	}
	tasks, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if !seen[task.Handle] && task.Repo == repo && task.Stage == "complete" && task.Review != nil {
			return nil
		}
	}
	return fmt.Errorf("coding task did not reach a completed review")
}
func savedTasks(home string) ([]managed.Task, error) {
	files, err := filepath.Glob(filepath.Join(home, ".config/doomer/managed/tasks/*.json"))
	if err != nil {
		return nil, err
	}
	var tasks []managed.Task
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var task managed.Task
		if err = json.Unmarshal(raw, &task); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}
func (j *journey) git(repo string, args ...string) error {
	cmd := exec.CommandContext(j.ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = j.env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fixture setup: %w: %s", err, out)
	}
	return nil
}
func (j *journey) finish() error {
	j.f.mu.Lock()
	creates := j.f.Creates
	reviews := len(j.f.Reviews)
	responses := len(j.f.Feedback)
	deepPolls := j.f.DeepPolls
	automatic := true
	groups := map[string]bool{}
	for _, review := range j.f.Reviews {
		groups[review.GroupID] = true
		automatic = automatic && review.FullReview == nil
	}
	j.f.mu.Unlock()
	j.result.Checks["grant_reused"] = creates == 1
	j.result.Checks["review_delivered"] = reviews > 0
	if j.c.Scenario == "first-review" || strings.HasPrefix(j.c.Scenario, "tui") {
		j.result.Checks["finding_response_recorded"] = responses > 0
	}
	if j.c.Scenario == "deep-review" {
		j.result.Checks["waited_for_deep"] = deepPolls >= 2
		j.result.Checks["automatic_review_selection"] = automatic
	}
	if j.c.Scenario == "new-chat" {
		j.result.Checks["two_reviews"] = len(groups) == 2
	}
	for _, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("scenario assertion failed")
		}
	}
	return nil
}
func (j *journey) afterInstall() error {
	switch j.c.Scenario {
	case "install", "interrupted":
		return nil
	case "portable-restart":
		return j.portableRestart()
	case "portable-invalid":
		return j.invalidPortable()
	case "interrupted-review":
		return j.interruptedReview()
	case "revoked-uploads":
		return j.revokedUploads()
	case "tui", "tui-hooks-denied":
		return j.tui()
	case "first-review", "new-chat", "personal", "hooks-off", "deep-review":
		if j.c.Scenario == "hooks-off" {
			if err := os.Remove(agentHooksPath(j.c, j.home)); err != nil {
				return err
			}
		}
		j.f.mu.Lock()
		j.f.Findings = j.c.Scenario == "first-review"
		j.f.Deep = j.c.Scenario == "deep-review"
		j.f.mu.Unlock()
		if err := j.coding(j.repo, "Create hello.txt containing hello followed by a newline. Follow the repository's installed instructions and complete the task."); err != nil {
			return err
		}
		content, readErr := os.ReadFile(filepath.Join(j.repo, "hello.txt"))
		if readErr != nil || string(content) != "hello\n" {
			return fmt.Errorf("greeting content mismatch")
		}
		if j.c.Scenario == "new-chat" {
			if err := j.coding(j.repo, "Create second.txt containing second followed by a newline. Follow the repository's installed instructions and complete the task."); err != nil {
				return err
			}
		}
		return j.finish()
	case "worktrees":
		if err := j.git(j.repo, "add", "."); err != nil {
			return err
		}
		if err := j.git(j.repo, "-c", "commit.gpgsign=false", "commit", "-qm", "Installed fixture"); err != nil {
			return err
		}
		roots := []string{filepath.Join(filepath.Dir(j.repo), "work-a"), filepath.Join(filepath.Dir(j.repo), "work-b")}
		for i, root := range roots {
			if err := j.git(j.repo, "worktree", "add", "-b", fmt.Sprintf("work-%d", i), root); err != nil {
				return err
			}
		}
		errors := make(chan error, 2)
		var wg sync.WaitGroup
		for i, root := range roots {
			wg.Add(1)
			go func(i int, root string) {
				defer wg.Done()
				errors <- j.coding(root, fmt.Sprintf("Create task-%d.txt containing worktree %d followed by a newline. Follow the repository's installed instructions and complete the task.", i, i))
			}(i, root)
		}
		wg.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				return err
			}
		}
		for i, root := range roots {
			raw, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("task-%d.txt", i)))
			if err != nil || string(raw) != fmt.Sprintf("worktree %d\n", i) {
				return fmt.Errorf("worktree content mismatch")
			}
		}
		j.f.mu.Lock()
		groups := map[string]bool{}
		matched := map[string]bool{}
		for _, review := range j.f.Reviews {
			groups[review.GroupID] = true
			for i := 0; i < 2; i++ {
				if review.Branch == fmt.Sprintf("work-%d", i) && strings.Contains(review.Diff, fmt.Sprintf("task-%d.txt", i)) {
					matched[review.Branch] = true
				}
			}
		}
		j.result.Checks["distinct_reviews"] = len(groups) == 2
		j.result.Checks["review_matches_worktree"] = len(matched) == 2
		j.f.mu.Unlock()
		return j.finish()
	case "repeat-onboarding", "read-only":
		if err := j.git(j.repo, "add", "."); err != nil {
			return err
		}
		if err := j.git(j.repo, "-c", "commit.gpgsign=false", "commit", "-qm", "Installed fixture"); err != nil {
			return err
		}
		prompt := "Set up Doomer in this repository again. Reuse existing setup and permissions. Do not change code."
		if j.c.Scenario == "read-only" {
			prompt = "Tell me the current branch and whether the working tree is clean. Do not edit files or start coding."
		}
		j.result.Turns++
		turn, err := runAgent(j.ctx, j.c, j.repo, j.env, prompt, "", j.record)
		if err != nil {
			return err
		}
		j.f.mu.Lock()
		j.result.Checks["no_new_grant"] = j.f.Creates == 1
		j.result.Checks["no_read_only_upload"] = j.f.Uploads == 0 && len(j.f.Reviews) == 0
		j.f.mu.Unlock()
		j.result.Checks["no_repeat_update_permission"] = !turn.UpdateRequested
		j.result.Checks["no_repeat_settings_proposal"] = !turn.PolicyOffered
		tasks, err := savedTasks(j.home)
		if err != nil {
			return err
		}
		j.result.Checks["no_read_only_failure"] = true
		for _, task := range tasks {
			if task.LastError != "" {
				j.result.Checks["no_read_only_failure"] = false
			}
		}
		for _, ok := range j.result.Checks {
			if !ok {
				return fmt.Errorf("read-only or repeated onboarding assertion failed")
			}
		}
		return nil
	case "service-failure":
		j.f.mu.Lock()
		j.f.FailSubmit = true
		j.f.mu.Unlock()
		j.result.Turns++
		_, err := runAgent(j.ctx, j.c, j.repo, j.env, "Create hello.txt containing hello followed by a newline. Follow the repository's installed instructions and complete the task.", "", j.record)
		if err != nil {
			return err
		}
		tasks, err := savedTasks(j.home)
		if err != nil {
			return err
		}
		var failed managed.Task
		for _, task := range tasks {
			if task.LastError != "" {
				failed = task
			}
		}
		if failed.Handle == "" {
			return fmt.Errorf("failure was not retained")
		}
		j.f.mu.Lock()
		j.result.Checks["bounded_failure"] = len(j.f.Attempts) <= 3
		j.f.FailSubmit = false
		j.f.mu.Unlock()
		j.result.Turns++
		_, err = runAgent(j.ctx, j.c, j.repo, j.env, "The service has recovered. Retry the existing Doomer task "+failed.Handle+" and complete its review. Preserve its ownership.", "", j.record)
		if err != nil {
			return err
		}
		tasks, err = savedTasks(j.home)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if task.Handle == failed.Handle && task.Stage == "complete" {
				j.result.Checks["same_task_recovered"] = true
			}
		}
		if !j.result.Checks["same_task_recovered"] {
			return fmt.Errorf("failed task did not recover")
		}
		return j.finish()
	case "upgrade", "upgrade-defer", "upgrade-untracked", "upgrade-personal", "upgrade-custom":
		return j.upgrade()
	}
	return fmt.Errorf("unsupported post-install scenario")
}

func personalScenario(name string) bool { return name == "personal" || name == "upgrade-personal" }
