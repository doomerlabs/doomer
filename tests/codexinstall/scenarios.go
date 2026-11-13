package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// The matrix describes only the Node-managed onboarding contract. Legacy
// managed installers, upgrades, portable state and policy recovery are retired.
var scenarioNames = []string{"install", "skill-first", "standalone", "decline-uploads", "repeat-onboarding", "hook-repair", "first-review", "new-chat", "read-only", "nested-repositories", "service-isolation", "uninstall", "hooks-declined"}

func validScenario(name string) bool {
	for _, candidate := range scenarioNames {
		if name == candidate {
			return true
		}
	}
	return false
}

type journey struct {
	c          config
	ctx        context.Context
	home, repo string
	env        []string
	f          *fixture
	result     *report
	record     func(string, string)
}

func runAgent(ctx context.Context, c config, repo string, env []string, prompt, session string, record func(string, string)) (codexTurn, error) {
	if c.Harness == "claude" {
		return runClaude(ctx, c, repo, env, prompt, session, record)
	}
	record("Scripted user", prompt)
	args := codexExecArgs(c)
	if c.ReviewApprovals {
		record("Reviewer configuration", strings.Join(args, " "))
	}
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
	// Desktop Codex question cards are function calls, not agent_message items.
	// Include their user-visible titles when assessing an onboarding offer.
	for _, question := range codexUserQuestions(env, observed.Session) {
		observed.Messages = append(observed.Messages, question)
		record("User question", question)
	}
	return observed, nil
}

func codexExecArgs(c config) []string {
	if !c.ReviewApprovals {
		return []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox"}
	}
	return []string{"exec", "--json", "--sandbox", "workspace-write", "-c", `approval_policy="on-request"`, "-c", `approvals_reviewer="auto_review"`}
}
