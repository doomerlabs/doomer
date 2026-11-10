// cliimpact runs a bounded command-dependency experiment against the real CLI.
// Its fingerprints are diagnostic and are never published to the E2E cache.
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
	"time"

	"github.com/doomerlabs/doomer/internal/e2eimpact"
)

const trap = "command-dependency-probe-reached-handler"

type observation struct {
	Handler         *e2eimpact.ProbeHandler `json:"mutated_handler,omitempty"`
	FullFingerprint string                  `json:"full_fingerprint"`
	Projected       string                  `json:"experimental_fingerprint"`
	Succeeded       bool                    `json:"command_succeeded"`
	MatchedBaseline bool                    `json:"matched_baseline"`
	ReachedTrap     bool                    `json:"reached_trap"`
	Elapsed         string                  `json:"elapsed"`
}

type report struct {
	CacheEligible bool          `json:"cache_eligible"`
	Command       []string      `json:"command"`
	Observations  []observation `json:"observations"`
	Limitation    string        `json:"limitation"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rootBytes, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(rootBytes))
	listing := exec.CommandContext(ctx, "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	listing.Dir = root
	paths, err := listing.Output()
	if err != nil {
		return err
	}
	sources := map[string][]byte{}
	for _, name := range strings.Split(string(paths), "\x00") {
		if name == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return err
		}
		sources[name] = data
	}
	temp, err := os.MkdirTemp("", "doomer-cli-impact-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	home := filepath.Join(temp, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	// Both overlays replace only a handler body. The actual main, command
	// dispatch, constructors, hooks and startup code execute in every probe.
	omitted := e2eimpact.ProbeHandler{File: "cmd/whoami.go", Function: "newWhoamiCommand", Field: "RunE"}
	selected := e2eimpact.ProbeHandler{File: "cmd/version.go", Function: "newVersionCommand", Field: "RunE"}
	out := report{Command: []string{"version", "--format", "json"}, Limitation: "One fixed command in one environment. This does not prove an agent cannot call whoami, or authorize any cached E2E result."}
	var baseline []byte
	for _, handler := range []*e2eimpact.ProbeHandler{nil, &omitted, &selected} {
		started := time.Now()
		inputs := make(map[string][]byte, len(sources))
		for name, data := range sources {
			inputs[name] = data
		}
		args := []string{"build", "-mod=readonly", "-trimpath", "-o", filepath.Join(temp, "doomer")}
		if handler != nil {
			data, err := e2eimpact.TrapProbeHandler(sources[handler.File], *handler, trap)
			if err != nil {
				return err
			}
			inputs[handler.File] = data
			modified := filepath.Join(temp, "replacement.go")
			if err := os.WriteFile(modified, data, 0600); err != nil {
				return err
			}
			overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(root, handler.File): modified}})
			if err != nil {
				return err
			}
			overlayPath := filepath.Join(temp, "overlay.json")
			if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
				return err
			}
			args = append(args, "-overlay", overlayPath)
		}
		args = append(args, ".")
		build := exec.CommandContext(ctx, "go", args...)
		build.Dir = root
		build.Env = append(os.Environ(), "GOWORK=off")
		if output, err := build.CombinedOutput(); err != nil {
			return fmt.Errorf("build probe: %w\n%s", err, output)
		}
		probeCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		command := exec.CommandContext(probeCtx, filepath.Join(temp, "doomer"), out.Command...)
		command.Dir = home
		command.Env = isolatedEnv(home)
		output, commandErr := command.CombinedOutput()
		stop()
		full, err := e2eimpact.ProbeFingerprint(inputs, nil)
		if err != nil {
			return err
		}
		projected, err := e2eimpact.ProbeFingerprint(inputs, &omitted)
		if err != nil {
			return err
		}
		item := observation{Handler: handler, FullFingerprint: full, Projected: projected, Succeeded: commandErr == nil, ReachedTrap: bytes.Contains(output, []byte(trap)), Elapsed: time.Since(started).Round(time.Millisecond).String()}
		if handler == nil {
			if commandErr != nil || !json.Valid(output) {
				return fmt.Errorf("baseline version probe failed: %v\n%s", commandErr, output)
			}
			baseline = output
		}
		item.MatchedBaseline = bytes.Equal(output, baseline)
		out.Observations = append(out.Observations, item)
	}
	base, unrelated, relevant := out.Observations[0], out.Observations[1], out.Observations[2]
	if !unrelated.Succeeded || !unrelated.MatchedBaseline || unrelated.ReachedTrap || unrelated.FullFingerprint == base.FullFingerprint || unrelated.Projected != base.Projected {
		return fmt.Errorf("unrelated-handler mutation did not satisfy the bounded independence proof")
	}
	if relevant.Succeeded || !relevant.ReachedTrap || relevant.Projected == base.Projected {
		return fmt.Errorf("selected-handler mutation did not invalidate and reach the trap")
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

func isolatedEnv(home string) []string {
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key == "HOME" || key == "USERPROFILE" || key == "XDG_CONFIG_HOME" || key == "XDG_CACHE_HOME" || key == "XDG_DATA_HOME" || strings.HasPrefix(key, "DOOMER_") {
			continue
		}
		env = append(env, value)
	}
	return append(env, "HOME="+home, "USERPROFILE="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"), "XDG_DATA_HOME="+filepath.Join(home, "data"))
}
