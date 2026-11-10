package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/e2eimpact"
)

// This is the existing CI smoke contract, with the same commands/assertions.
// Agents never use this restricted artifact or its separate cache namespace.
type cliSmokeResult struct {
	Schema      int             `json:"schema"`
	Fingerprint string          `json:"fingerprint"`
	Binary      string          `json:"binary_sha256"`
	Runner      string          `json:"runner_sha256"`
	OS          string          `json:"os"`
	Arch        string          `json:"arch"`
	Bounded     bool            `json:"bounded"`
	Started     time.Time       `json:"started"`
	Ended       time.Time       `json:"ended"`
	Checks      map[string]bool `json:"checks"`
	Error       string          `json:"error,omitempty"`
	CacheHit    bool            `json:"cache_hit"`
}

func cliSmokeFingerprint(binary, runner string) string {
	raw, _ := json.Marshal([]string{"cli-smoke-v1", binary, runner, runtime.GOOS, runtime.GOARCH})
	return byteHash(raw)
}

func buildCLISmoke(ctx context.Context, source, temp string) (string, bool, error) {
	// Go resolves source directories before applying overlay keys (notably
	// /var -> /private/var on macOS), so keys must use that same canonical path.
	var err error
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", false, err
	}
	// Type-check the original package first: a removed body must never hide an
	// invalid full product build. Read dependencies from the isolated snapshot.
	check := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-deps", "-export", "./cmd")
	check.Dir = source
	check.Env = cliSmokeBuildEnv()
	if output, err := check.CombinedOutput(); err != nil {
		return "", false, fmt.Errorf("type-check original CLI: %w\n%s", err, output)
	}
	name := "doomer"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(temp, name)
	build := func(overlay string) error {
		args := []string{"build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-buildid= -X github.com/doomerlabs/doomer/internal/version.Version=ci-smoke", "-o", binary}
		if overlay != "" {
			args = append(args, "-overlay", overlay)
		}
		command := exec.CommandContext(ctx, "go", append(args, ".")...)
		command.Dir, command.Env = source, cliSmokeBuildEnv()
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("build CLI smoke: %w\n%s", err, output)
		}
		return nil
	}
	handler := e2eimpact.ProbeHandler{File: "cmd/whoami.go", Function: "newWhoamiCommand", Field: "RunE"}
	original, err := os.ReadFile(filepath.Join(source, handler.File))
	if err != nil {
		return "", false, err
	}
	bounded, projectionErr := e2eimpact.BoundedHandlerSource(original, handler)
	if projectionErr == nil {
		replacement := filepath.Join(temp, "whoami.go")
		if err := os.WriteFile(replacement, bounded, 0600); err != nil {
			return "", false, err
		}
		raw, err := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(source, handler.File): replacement}})
		if err != nil {
			return "", false, err
		}
		overlay := filepath.Join(temp, "overlay.json")
		if err := os.WriteFile(overlay, raw, 0600); err != nil {
			return "", false, err
		}
		if err := build(overlay); err == nil {
			return binary, true, nil
		}
	}
	// Unsupported constructors/imports never create an optimistic fingerprint.
	// The full binary remains a usable, conservatively keyed smoke test.
	fmt.Fprintln(os.Stderr, "CLI smoke: using full product build (bounded projection unavailable)")
	return binary, false, build("")
}

func cliSmokeBuildEnv() []string {
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "GOFLAGS", "GOWORK", "CGO_ENABLED", "GOOS", "GOARCH":
			continue
		}
		env = append(env, item)
	}
	return append(env, "GOFLAGS=", "GOWORK=off", "CGO_ENABLED=0", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
}

func exerciseCLISmoke(ctx context.Context, binary, home string, result *cliSmokeResult) error {
	config := filepath.Join(home, "config")
	command := func(input string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = home
		cmd.Env = []string{"HOME=" + home, "USERPROFILE=" + home, "PATH=/usr/bin:/bin", "DOOMER_CONFIG_DIR=" + config, "NO_COLOR=1"}
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("CLI smoke %v: %w\n%s", args, err, output)
		}
		return string(output), nil
	}
	version, err := command("", "version")
	if err != nil {
		return err
	}
	result.Checks["version"] = strings.Contains(version, "ci-smoke")
	for _, args := range [][]string{{"profile", "add", "smoke", "--endpoint", "https://doomer.ai/api"}, {"profile", "use", "smoke"}} {
		if _, err := command("", args...); err != nil {
			return err
		}
	}
	if _, err := command("smoke-token\n", "login", "--token-stdin"); err != nil {
		return err
	}
	result.Checks["login"] = true
	profiles, err := command("", "profile", "ls")
	if err != nil {
		return err
	}
	result.Checks["profile_selection"] = strings.Contains(profiles, "* smoke")
	if _, err := command("", "logout", "--local-only"); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(config, "config.json"))
	if err != nil {
		return err
	}
	result.Checks["logout_removes_token"] = !strings.Contains(string(data), "smoke-token")
	for name, passed := range result.Checks {
		if !passed {
			return fmt.Errorf("CLI smoke assertion failed: %s", name)
		}
	}
	return nil
}

func runCLISmoke(ctx context.Context, source, output string, noCache, cacheOnly bool) error {
	if source == "" {
		source = "."
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if os.Getenv("GOFLAGS") != "" {
		return fmt.Errorf("CLI smoke requires empty GOFLAGS; external overlays are not supported")
	}
	temp, err := os.MkdirTemp("", "doomer-cli-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if _, err := copySource(ctx, source, temp, "source", output); err != nil {
		return err
	}
	binary, bounded, err := buildCLISmoke(ctx, filepath.Join(temp, "source"), temp)
	if err != nil {
		return err
	}
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	runnerBytes, err := os.ReadFile(executable)
	if err != nil {
		return err
	}
	result := cliSmokeResult{Schema: 1, Binary: byteHash(binaryBytes), Runner: byteHash(runnerBytes), OS: runtime.GOOS, Arch: runtime.GOARCH, Bounded: bounded, Checks: map[string]bool{}}
	result.Fingerprint = cliSmokeFingerprint(result.Binary, result.Runner)
	var cache *cliSmokeCache
	if !noCache {
		cache = openCLISmokeCache()
		if cache != nil {
			if cached, err := cache.load(ctx, result); err == nil {
				cached.CacheHit = true
				fmt.Fprintln(os.Stderr, "CLI smoke cache hit:", cached.Fingerprint, "(no smoke commands executed)")
				return writeCLISmokeResult(output, cached)
			}
		}
	}
	if cacheOnly {
		return fmt.Errorf("CLI smoke cache miss: %s", result.Fingerprint)
	}
	fmt.Fprintln(os.Stderr, "CLI smoke executing:", result.Fingerprint)
	home := filepath.Join(temp, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		return err
	}
	result.Started = time.Now().UTC()
	executionErr := exerciseCLISmoke(ctx, binary, home, &result)
	result.Ended = time.Now().UTC()
	if executionErr != nil {
		result.Error = executionErr.Error()
	}
	if err := writeCLISmokeResult(output, result); err != nil {
		return err
	}
	if executionErr != nil {
		return executionErr
	}
	if cache != nil {
		if err := cache.publish(ctx, result); err != nil {
			fmt.Fprintln(os.Stderr, "CLI smoke cache publication skipped:", err)
		}
	}
	return nil
}

func writeCLISmokeResult(path string, result cliSmokeResult) error {
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0600)
}
