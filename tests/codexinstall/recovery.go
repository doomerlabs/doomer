package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

func (j *journey) invalidPortable() error {
	tasks, err := savedTasks(j.home)
	if err != nil || len(tasks) != 1 {
		return fmt.Errorf("expected original setup task")
	}
	handle := tasks[0].Handle
	cmd := exec.CommandContext(j.ctx, filepath.Join(j.home, ".local/bin/doomer"), "prepare", "--task", handle, "--portable")
	cmd.Dir, cmd.Env = j.repo, j.env
	output, err := cmd.CombinedOutput()
	j.record("Command", "Request portable setup without an origin remote\n"+string(output))
	if err == nil {
		return fmt.Errorf("portable setup unexpectedly accepted a missing origin")
	}
	tasks, err = savedTasks(j.home)
	if err != nil || len(tasks) != 1 || tasks[0].Portable {
		return fmt.Errorf("failed portable request changed the task")
	}
	j.result.Turns++
	_, err = runAgent(j.ctx, j.c, j.repo, j.env, "The optional portable setup request failed because this local repository has no origin. Run doomer prepare --task "+handle+" in this same persistent home, without --portable, to verify the original ordinary setup. Preserve DOOMER_API_URL and reuse existing approvals without creating a replacement task.", "", j.record)
	if err != nil {
		return err
	}
	tasks, err = savedTasks(j.home)
	if err != nil || len(tasks) != 1 || tasks[0].Portable || tasks[0].LastError != "" {
		return fmt.Errorf("ordinary setup did not recover")
	}
	j.result.Checks["failed_portable_request_recovered"] = true
	return nil
}

func (j *journey) revokedUploads() error {
	j.f.mu.Lock()
	j.f.Approved = false
	j.f.Request = ""
	j.f.mu.Unlock()
	j.result.Turns++
	_, err := runAgent(j.ctx, j.c, j.repo, j.env, "Create hello.txt containing hello. Follow the installed Doomer skill and complete its review.", "", j.record)
	if err != nil {
		return err
	}
	j.f.mu.Lock()
	j.result.Checks["revocation_blocks_uploads"] = j.f.Uploads == 0 && len(j.f.Reviews) == 0 && !j.f.Approved
	j.f.mu.Unlock()
	if !j.result.Checks["revocation_blocks_uploads"] {
		return fmt.Errorf("revoked repository still uploaded content")
	}
	return nil
}

func (j *journey) interruptedReview() error {
	j.f.mu.Lock()
	j.f.PendingReviews = true
	j.f.mu.Unlock()
	ctx, cancel := context.WithCancel(j.ctx)
	defer cancel()
	done := make(chan error, 1)
	j.result.Turns++
	go func() {
		_, err := runAgent(ctx, j.c, j.repo, j.env, "Create hello.txt containing hello followed by a newline. Follow the installed Doomer skill and complete the review.", "", j.record)
		done <- err
	}()
	var handle string
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
wait:
	for {
		select {
		case err := <-done:
			return fmt.Errorf("agent stopped before the review interruption point: %v", err)
		case <-timer.C:
			break wait
		case <-j.ctx.Done():
			break wait
		case <-ticker.C:
			tasks, err := savedTasks(j.home)
			if err != nil {
				continue
			}
			for _, task := range tasks {
				if task.Stage == "review_pending" && task.Review != nil {
					handle = task.Handle
					break wait
				}
			}
		}
	}
	cancel()
	<-done
	if handle == "" {
		return fmt.Errorf("review did not reach the interruption point")
	}
	j.record("Fixture", "Terminated "+agentName(j.c)+" while its submitted review was pending. Resuming in a new process.")
	j.f.mu.Lock()
	j.f.PendingReviews = false
	j.f.mu.Unlock()
	j.result.Turns++
	_, err := runAgent(j.ctx, j.c, j.repo, j.env, "The previous process ended, but this is the same persistent test home and repository. Preserve DOOMER_API_URL. Run doomer prepare --task "+handle+" and finish that existing review without submitting a replacement.", "", j.record)
	if err != nil {
		return err
	}
	tasks, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.Handle == handle && task.Stage == "complete" {
			j.result.Checks["interrupted_review_recovered"] = true
		}
	}
	j.f.mu.Lock()
	j.result.Checks["no_duplicate_submission"] = len(j.f.Attempts) == 1 && len(j.f.Reviews) == 1
	j.f.mu.Unlock()
	if !j.result.Checks["interrupted_review_recovered"] || !j.result.Checks["no_duplicate_submission"] {
		return fmt.Errorf("interrupted review did not resume its original submission")
	}
	return j.finish()
}
