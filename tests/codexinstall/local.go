package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
)

func runLocal(ctx context.Context, c config, parent string) (result report) {
	if c.Scenario == "global-npx-discovery" {
		// The public npx skill requires the current bootstrap-compatible runtime.
		c.CandidateVersion = managed.BootstrapVersion
	}
	result.Execution = "local"
	if parent == "" {
		result.Error = "local runs require --local-root"
		return result
	}
	if err := validateAuthentication(c); err != nil {
		result.Error = err.Error()
		return result
	}
	dir, err := os.MkdirTemp(parent, "doomer-local-")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() {
		processErr := stopLocalProcesses(dir)
		removeErr := os.RemoveAll(dir)
		result.LocalCleaned = processErr == nil && removeErr == nil
		if !result.LocalCleaned {
			result.Passed = false
			result.Error += "; local cleanup failed"
		}
	}()
	c.WorkRoot = dir
	if c.CandidateVersion != "" {
		c.CandidateFile = filepath.Join(dir, "candidate")
		overlay, err := candidateReleaseOverlay(c.CandidateSource, dir, c.CandidateVersion)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		build := exec.CommandContext(ctx, "go", "build", "-overlay", overlay, "-ldflags", "-X github.com/doomerlabs/doomer/internal/version.Version="+c.CandidateVersion+" -X github.com/doomerlabs/doomer/internal/managed.BootstrapCLIRange=>="+c.CandidateVersion, "-o", c.CandidateFile, ".")
		build.Dir = c.CandidateSource
		if output, err := build.CombinedOutput(); err != nil {
			result.Error = fmt.Sprintf("build local candidate: %v: %s", err, output)
			return result
		}
	}
	result = runWorkerContext(ctx, c)
	result.Execution = "local"
	return result
}

// Codex TUI may leave its managed daemon running after the client exits.
// Restrict cleanup to executables located inside this run's private directory.
func stopLocalProcesses(dir string) error {
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
		cancel()
		if err != nil {
			return fmt.Errorf("inspect local test processes: %w", err)
		}
		pids := localProcessIDs(string(output), dir)
		if len(pids) == 0 {
			return nil
		}
		if attempt == 2 {
			return fmt.Errorf("local test processes survived cleanup")
		}
		for _, pid := range pids {
			process, err := os.FindProcess(pid)
			if err == nil {
				_ = process.Kill()
				_ = process.Release()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

func localProcessIDs(output, dir string) []int {
	var pids []int
	prefix := filepath.Clean(dir) + string(os.PathSeparator)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		separator := strings.IndexAny(line, " \t")
		if separator < 0 || !strings.HasPrefix(strings.TrimSpace(line[separator:]), prefix) {
			continue
		}
		if pid, err := strconv.Atoi(line[:separator]); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
