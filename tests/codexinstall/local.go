package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func runLocal(ctx context.Context, c config, parent string) (result report) {
	result.Execution = "local"
	if parent == "" || c.APIKey == "" {
		result.Error = "local runs require --local-root and TEST_OPENAI_API_KEY"
		return result
	}
	dir, err := os.MkdirTemp(parent, "doomer-local-")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() {
		result.LocalCleaned = os.RemoveAll(dir) == nil
		if !result.LocalCleaned {
			result.Passed = false
			result.Error += "; local cleanup failed"
		}
	}()
	c.WorkRoot = dir
	if c.CandidateVersion != "" {
		c.CandidateFile = filepath.Join(dir, "candidate")
		build := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X github.com/doomerlabs/doomer/internal/version.Version="+c.CandidateVersion, "-o", c.CandidateFile, ".")
		if output, err := build.CombinedOutput(); err != nil {
			result.Error = fmt.Sprintf("build local candidate: %v: %s", err, output)
			return result
		}
	}
	result = runWorker(c)
	result.Execution = "local"
	return result
}
