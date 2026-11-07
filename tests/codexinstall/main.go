// Command codexinstall runs the real Codex CLI install journey in a disposable exe.dev VM.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type config struct {
	WorkRoot         string  `json:"work_root,omitempty"`
	CandidateFile    string  `json:"candidate_file,omitempty"`
	CandidateVersion string  `json:"candidate_version,omitempty"`
	APIKey           string  `json:"api_key"`
	Model            string  `json:"model"`
	Budget           float64 `json:"budget_usd"`
	Codex            string  `json:"codex"`
	Scenario         string  `json:"scenario"`
	LegacyBootstrap  string  `json:"legacy_bootstrap,omitempty"`
}

type recordedEvent struct {
	Seconds float64 `json:"seconds"`
	Kind    string  `json:"kind"`
	Text    string  `json:"text"`
}

type report struct {
	Execution        string          `json:"execution"`
	LocalCleaned     bool            `json:"local_cleaned,omitempty"`
	Scenario         string          `json:"scenario"`
	CandidateVersion string          `json:"candidate_version,omitempty"`
	DoomerVersion    string          `json:"doomer_version,omitempty"`
	Passed           bool            `json:"passed"`
	Error            string          `json:"error,omitempty"`
	Model            string          `json:"model"`
	CodexVersion     string          `json:"codex_version"`
	VM               string          `json:"vm,omitempty"`
	Deleted          bool            `json:"vm_deleted"`
	Duration         float64         `json:"duration_seconds"`
	Usage            usage           `json:"usage"`
	Turns            int             `json:"turns"`
	APICalls         []string        `json:"api_calls"`
	Checks           map[string]bool `json:"checks"`
	Events           []recordedEvent `json:"events"`
}

func main() {
	localCodex := flag.String("local-codex", "", "dedicated pinned Codex executable for an isolated local run")
	localRoot := flag.String("local-root", "", "writable parent directory for disposable local test homes")
	candidate := flag.String("candidate-release", "", "test this checkout as the given installed release after public installation")
	scenario := flag.String("scenario", "install", "manual scenario name; use list to show available scenarios")
	render := flag.String("render-report", "", "render an existing JSON report to PATH.html without running a test")
	worker := flag.Bool("worker", false, "internal VM worker mode")
	model := flag.String("model", "gpt-6.1-terra", "model under test")
	version := flag.String("codex-version", "0.167.0", "exact supported Codex release version")
	budget := flag.Float64("budget", .20, "maximum reserved API spend for this run, USD")
	output := flag.String("output", "codex-install-report.json", "sanitized JSON report")
	flag.Parse()
	if *scenario == "list" {
		fmt.Println(strings.Join(scenarioNames, "\n"))
		return
	}
	if !validScenario(*scenario) {
		fmt.Fprintln(os.Stderr, "unknown scenario")
		os.Exit(1)
	}
	if *render != "" {
		data, err := os.ReadFile(*render)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var r report
		if err := json.Unmarshal(data, &r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := writeReplay(*render+".html", r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(*render + ".html")
		return
	}
	if *worker {
		var c config
		if err := json.NewDecoder(os.Stdin).Decode(&c); err != nil {
			fmt.Fprintln(os.Stderr, "invalid worker input")
			os.Exit(1)
		}
		r := runWorker(c)
		_ = json.NewEncoder(os.Stdout).Encode(r)
		return
	}
	if *model != "gpt-6.1-terra" || *budget <= 0 || *budget > 1 {
		fmt.Fprintln(os.Stderr, "this experiment supports gpt-6.1-terra and a budget above zero and at most $1")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	c := config{APIKey: os.Getenv("TEST_OPENAI_API_KEY"), Model: *model, Budget: *budget, Scenario: *scenario, CandidateVersion: *candidate}
	if strings.HasPrefix(*scenario, "upgrade") {
		raw, err := os.ReadFile("internal/managed/bootstrap-2026.11.6.10.md")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		c.LegacyBootstrap = string(raw)
	}
	var r report
	if *localCodex != "" {
		c.Codex = *localCodex
		r = runLocal(ctx, c, *localRoot)
	} else {
		r = runRemote(ctx, c, *version)
	}
	data, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(*output, append(data, '\n'), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := writeReplay(*output+".html", r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("passed=%t cost=$%.6f reserved=$%.6f vm_deleted=%t report=%s\n", r.Passed, r.Usage.Cost, r.Usage.Reserved, r.Deleted, *output)
	if !r.Passed || (r.VM != "" && !r.Deleted) || (r.Execution == "local" && !r.LocalCleaned) {
		os.Exit(1)
	}
}
