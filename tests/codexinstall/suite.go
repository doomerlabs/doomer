package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type suiteCase struct {
	Scenario string   `json:"scenario"`
	Passed   bool     `json:"passed"`
	CacheHit bool     `json:"cache_hit,omitempty"`
	Status   string   `json:"status"`
	Report   string   `json:"report"`
	Log      string   `json:"log"`
	Error    string   `json:"error,omitempty"`
	Duration float64  `json:"duration_seconds"`
	Charged  *float64 `json:"budget_charged_usd,omitempty"`
}

type suiteSummary struct {
	AuthMode string      `json:"auth_mode"`
	Jobs     int         `json:"jobs"`
	Duration float64     `json:"duration_seconds"`
	Charged  *float64    `json:"budget_charged_usd,omitempty"`
	Results  []suiteCase `json:"results"`
}

type suiteRunner func(context.Context, string, string, string) suiteCase

func selectSuiteScenarios(value string) ([]string, error) {
	if value == "" {
		return append([]string(nil), scenarioNames...), nil
	}
	var names []string
	seen := map[string]bool{}
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		if !validScenario(name) || seen[name] {
			return nil, fmt.Errorf("invalid or duplicate suite scenario %q", name)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

func runSuite(ctx context.Context, output string, perRun, totalLimit float64, auth string, jobs int, names []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return executeSuite(ctx, output, perRun, totalLimit, auth, jobs, names, func(ctx context.Context, scenario, path, logPath string) suiteCase {
		args := append(append([]string{}, os.Args[1:]...), "--scenario", scenario, "--output", path)
		return runSuiteCommand(ctx, executable, args, scenario, path, logPath, perRun, auth)
	})
}

func runSuiteCommand(ctx context.Context, executable string, args []string, scenario, path, logPath string, perRun float64, auth string) suiteCase {
	result := suiteCase{Scenario: scenario, Report: path, Log: logPath, Status: "failed"}
	start := time.Now()
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer log.Close()
	command := exec.CommandContext(ctx, executable, args...)
	// Give the runner time to cancel inference, stop local agents or delete its VM,
	// and write the final report before resorting to a hard kill.
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 60 * time.Second
	command.Stdout, command.Stderr = log, log
	runErr := command.Run()
	result.Duration = time.Since(start).Seconds()
	result.Charged = new(float64)
	*result.Charged = perRun // Unknown outcomes retain the entire reservation.
	data, readErr := os.ReadFile(path)
	var r report
	if readErr == nil {
		readErr = json.Unmarshal(data, &r)
	}
	if readErr == nil && r.Scenario == scenario && r.AuthMode == auth {
		var accounting struct {
			Usage *struct {
				Charged *float64 `json:"budget_charged_usd"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(data, &accounting)
		charged := perRun
		if accounting.Usage != nil && accounting.Usage.Charged != nil {
			charged = *accounting.Usage.Charged
		}
		if r.Cache != nil && r.Cache.Hit {
			charged = 0
			result.CacheHit = true
		}
		validCharge := accounting.Usage != nil && accounting.Usage.Charged != nil && !math.IsNaN(charged) && !math.IsInf(charged, 0) && charged >= 0 && charged <= perRun
		if validCharge {
			*result.Charged = charged
		}
		result.Error = r.Error
		result.Passed = runErr == nil && r.Passed && (auth != "api" || validCharge)
		if auth == "api" && !validCharge {
			result.Error = strings.TrimSpace(result.Error + "; invalid or missing budget accounting")
		}
	} else {
		result.Error = fmt.Sprintf("missing or invalid scenario report: %v", readErr)
	}
	if runErr != nil {
		result.Error = strings.TrimSpace(result.Error + "; " + runErr.Error())
	}
	if ctx.Err() != nil {
		result.Status = "cancelled"
		result.Passed = false
	} else if result.Passed {
		result.Status = "passed"
	}
	return result
}

// Only this coordinator owns results and budget state. Every running case holds
// its full reservation until a valid report reconciles the actual charge.
func executeSuite(ctx context.Context, output string, perRun, totalLimit float64, auth string, jobs int, names []string, run suiteRunner) error {
	if jobs < 1 {
		return fmt.Errorf("--jobs must be positive")
	}
	if auth == "api" && (math.IsNaN(perRun) || math.IsInf(perRun, 0) || perRun <= 0 || perRun > 1 || math.IsNaN(totalLimit) || math.IsInf(totalLimit, 0) || totalLimit <= 0) {
		return fmt.Errorf("invalid suite budget")
	}
	if _, err := os.Stat(output); err == nil {
		return fmt.Errorf("existing report %s; choose a new output prefix", output)
	} else if !os.IsNotExist(err) {
		return err
	}
	root := strings.TrimSuffix(output, filepath.Ext(output)) + "-runs"
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	start := time.Now()
	summary := suiteSummary{AuthMode: auth, Jobs: jobs, Results: make([]suiteCase, len(names))}
	for i, name := range names {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		summary.Results[i] = suiteCase{Scenario: name, Status: "unstarted", Report: filepath.Join(dir, "report.json"), Log: filepath.Join(dir, "runner.log")}
	}
	spent, reserved := 0.0, 0.0
	write := func() error {
		summary.Duration = time.Since(start).Seconds()
		if auth == "api" {
			summary.Charged = &spent
		}
		data, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return err
		}
		// Readers always see a complete old or new aggregate, never a partial write.
		tmp := output + ".tmp"
		if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
			return err
		}
		return os.Rename(tmp, output)
	}
	type completion struct {
		index  int
		result suiteCase
	}
	done := make(chan completion, jobs)
	next, running := 0, 0
	var writeErr error
	for next < len(names) || running > 0 {
		for ctx.Err() == nil && next < len(names) && running < jobs && (auth != "api" || spent+reserved+perRun <= totalLimit) {
			i := next
			next++
			running++
			if auth == "api" {
				reserved += perRun
			}
			item := summary.Results[i]
			summary.Results[i].Status = "running"
			go func() { done <- completion{i, run(ctx, item.Scenario, item.Report, item.Log)} }()
		}
		if err := write(); err != nil && writeErr == nil {
			writeErr = err
			cancel()
		}
		if running == 0 {
			break
		}
		completed := <-done
		running--
		result := completed.result
		if auth == "api" {
			charged := perRun
			if result.Charged != nil && !math.IsNaN(*result.Charged) && !math.IsInf(*result.Charged, 0) && *result.Charged >= 0 && *result.Charged <= perRun {
				charged = *result.Charged
			}
			spent += charged
			reserved -= perRun
			// Avoid accumulated floating-point residue in outstanding reservations.
			reserved = float64(running) * perRun
			result.Charged = &charged
		} else {
			result.Charged = nil
		}
		summary.Results[completed.index] = result
		fmt.Fprintf(os.Stderr, "%s: %s (%.1fs)\n", result.Scenario, result.Status, result.Duration)
	}
	failed, unstarted := 0, 0
	for i := range summary.Results {
		r := &summary.Results[i]
		if r.Status == "unstarted" {
			unstarted++
			r.Error = "remaining suite budget cannot cover reservation"
			if ctx.Err() != nil {
				r.Error = ctx.Err().Error()
			}
		} else if !r.Passed {
			failed++
		}
	}
	if err := write(); err != nil && writeErr == nil {
		writeErr = err
	}
	fmt.Fprintf(os.Stderr, "Suite: %.1fs wall time, %d passed, %d failed, %d unstarted\n", summary.Duration, len(names)-failed-unstarted, failed, unstarted)
	slowest := append([]suiteCase(nil), summary.Results...)
	sort.SliceStable(slowest, func(i, j int) bool { return slowest[i].Duration > slowest[j].Duration })
	for i, r := range slowest {
		if i == 5 || r.Duration == 0 {
			break
		}
		fmt.Fprintf(os.Stderr, "Slowest: %s %.1fs\n", r.Scenario, r.Duration)
	}
	if writeErr != nil {
		return writeErr
	}
	if failed > 0 || unstarted > 0 {
		return fmt.Errorf("suite incomplete or failed; see %s", output)
	}
	return ctx.Err()
}
