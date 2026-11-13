// Command codexinstall exercises Node-managed onboarding with real agents in disposable homes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type config struct {
	ToolingSource        string  `json:"tooling_source"`
	NodeSource           string  `json:"node_source"`
	SkillsSource         string  `json:"skills_source"`
	SkillsCLIVersion     string  `json:"skills_cli_version"`
	CodexAuthHome        string  `json:"codex_auth_home,omitempty"`
	AgentVersion         string  `json:"agent_version,omitempty"`
	ReviewApprovals      bool    `json:"review_approvals,omitempty"`
	ClaudePermissionMode string  `json:"claude_permission_mode,omitempty"`
	Harness              string  `json:"harness,omitempty"`
	AuthMode             string  `json:"auth_mode,omitempty"`
	OAuthToken           string  `json:"oauth_token,omitempty"`
	WorkRoot             string  `json:"work_root,omitempty"`
	CandidateSource      string  `json:"candidate_source,omitempty"`
	CandidateFile        string  `json:"candidate_file,omitempty"`
	CandidateVersion     string  `json:"candidate_version,omitempty"`
	APIKey               string  `json:"api_key"`
	Model                string  `json:"model"`
	Budget               float64 `json:"budget_usd"`
	Codex                string  `json:"codex"`
	Scenario             string  `json:"scenario"`
}

type recordedEvent struct {
	Seconds float64 `json:"seconds"`
	Kind    string  `json:"kind"`
	Text    string  `json:"text"`
}

type report struct {
	Cache            *cacheProvenance `json:"cache,omitempty"`
	Harness          string           `json:"harness,omitempty"`
	AuthMode         string           `json:"auth_mode,omitempty"`
	Execution        string           `json:"execution"`
	LocalCleaned     bool             `json:"local_cleaned,omitempty"`
	Scenario         string           `json:"scenario"`
	CandidateVersion string           `json:"candidate_version,omitempty"`
	DoomerVersion    string           `json:"doomer_version,omitempty"`
	Passed           bool             `json:"passed"`
	Error            string           `json:"error,omitempty"`
	Model            string           `json:"model"`
	CodexVersion     string           `json:"codex_version"`
	VM               string           `json:"vm,omitempty"`
	Deleted          bool             `json:"vm_deleted"`
	Duration         float64          `json:"duration_seconds"`
	Usage            usage            `json:"usage"`
	Turns            int              `json:"turns"`
	APICalls         []string         `json:"api_calls"`
	Checks           map[string]bool  `json:"checks"`
	Events           []recordedEvent  `json:"events"`
}

func main() {
	authHome := flag.String("codex-auth-home", "", "reuse a local Codex sign-in inside disposable test homes")
	harness := flag.String("harness", "codex", "agent harness: codex or claude")
	claudeMode := flag.String("claude-permission-mode", "auto", "Claude permission mode: auto or bypassPermissions")
	auth := flag.String("auth", "api", "authentication mode: api or subscription (local Codex sign-in or Claude)")
	localClaude := flag.String("local-claude", "", "dedicated pinned Claude executable for an isolated local run")
	nodeSource := flag.String("node-source", "", "captured Node manager repository")
	skillsSource := flag.String("skills-source", "", "captured new doomer skill repository")
	toolingSource := flag.String("tooling-source", "", "internal captured npm dependency lock")
	skillsVersion := flag.String("skills-cli-version", "", "exact official skills CLI version")
	localCodex := flag.String("local-codex", "", "dedicated pinned Codex executable for an isolated local run")
	localRoot := flag.String("local-root", "", "writable parent directory for disposable local test homes")
	candidateSource := flag.String("candidate-source", "", "local checkout to build as candidate; default is this checkout")
	candidate := flag.String("candidate-release", "", "test this checkout as the given installed release after public installation")
	scenario := flag.String("scenario", "install", "manual scenario name; use list to show available scenarios")
	render := flag.String("render-report", "", "render an existing JSON report to PATH.html without running a test")
	worker := flag.Bool("worker", false, "internal VM worker mode")
	model := flag.String("model", "", "model under test; defaults to the pinned harness model")
	version := flag.String("codex-version", "0.167.0", "exact supported Codex release version")
	snapshotInput := flag.String("snapshot-input", "", "internal captured input manifest")
	cacheOnly := flag.Bool("cache-only", false, "verify shared results without executing any agents; fail on a miss")
	noCache := flag.Bool("no-cache", false, "run every requested scenario without shared cache lookup or publication")
	jobs := flag.Int("jobs", 1, "maximum concurrent suite scenarios")
	suiteScenarios := flag.String("suite-scenarios", "", "comma-separated subset for --scenario all")
	suiteBudget := flag.Float64("suite-budget", 5, "maximum total API reservations when --scenario all is used")
	budget := flag.Float64("budget", .20, "maximum reserved API spend for this run, USD")
	output := flag.String("output", "codex-install-report.json", "sanitized JSON report")
	flag.Parse()
	if *harness == "claude" {
		budgetSpecified := false
		flag.Visit(func(f *flag.Flag) { budgetSpecified = budgetSpecified || f.Name == "budget" })
		if !budgetSpecified {
			*budget = 1
		}
	}
	if *scenario == "list" {
		fmt.Println(strings.Join(scenarioNames, "\n"))
		return
	}
	if err := validateAuthMode(*harness, *auth); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *localClaude != "" && *harness != "claude" {
		fmt.Fprintln(os.Stderr, "--local-claude requires --harness claude")
		os.Exit(1)
	}
	if *claudeMode != "auto" && *claudeMode != "bypassPermissions" {
		fmt.Fprintln(os.Stderr, "Claude permission mode must be auto or bypassPermissions")
		os.Exit(1)
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if !*worker && *render == "" && *snapshotInput == "" {
		settings := map[string]string{}
		excluded := map[string]bool{"output": true, "local-root": true, "local-codex": true, "local-claude": true, "codex-auth-home": true, "candidate-source": true, "skills-source": true, "node-source": true, "tooling-source": true, "scenario": true, "suite-scenarios": true, "jobs": true, "suite-budget": true, "no-cache": true, "cache-only": true, "snapshot-input": true, "render-report": true, "worker": true}
		flag.VisitAll(func(f *flag.Flag) {
			if !excluded[f.Name] {
				settings[f.Name] = f.Value.String()
			}
		})
		if settings["model"] == "" {
			settings["model"] = "gpt-6.1-terra"
			if *harness == "claude" {
				settings["model"] = "claude-opus-5-5"
			}
			if *authHome != "" {
				settings["model"] = "gpt-6.1-sol"
			}
		}
		settings["reasoning"] = "low"
		if *harness == "claude" {
			settings["reasoning"] = claudeEffort
		}
		settings["skills-source-selected"] = fmt.Sprint(*skillsSource != "")
		settings["candidate-source-selected"] = fmt.Sprint(*candidateSource != "")
		settings["execution"] = "local"
		if *localCodex != "" || *localClaude != "" {
			settings["execution"] = "local"
		}
		settings["subscription-sign-in"] = fmt.Sprint(*authHome != "")
		for _, name := range []string{"GOFLAGS", "CGO_ENABLED", "GOTOOLCHAIN"} {
			settings[name] = os.Getenv(name)
		}
		agent := *localCodex
		if *localClaude != "" {
			agent = *localClaude
		}
		if *localRoot != "" {
			absolute, err := filepath.Abs(*localRoot)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			*localRoot = absolute
			os.Args = append(os.Args, "--local-root", absolute)
		}
		if *authHome != "" {
			absolute, err := filepath.Abs(*authHome)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Args = append(os.Args, "--codex-auth-home", absolute)
		}
		options := snapshotOptions{Output: *output, Agent: agent, Candidate: *candidateSource, Node: *nodeSource, Skills: *skillsSource, Settings: settings, Local: agent != ""}
		if err := runSnapshot(signalCtx, options, os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *scenario == "all" {
		names, err := selectSuiteScenarios(*suiteScenarios)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := runSuite(signalCtx, *output, *budget, *suiteBudget, *auth, *jobs, names); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
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
		r := runWorkerContext(signalCtx, c)
		_ = json.NewEncoder(os.Stdout).Encode(r)
		return
	}
	if *harness != "codex" && *harness != "claude" {
		fmt.Fprintln(os.Stderr, "unknown harness")
		os.Exit(1)
	}
	expectedModel := "gpt-6.1-terra"
	keyName := "TEST_OPENAI_API_KEY"
	if *harness == "claude" {
		expectedModel = "claude-opus-5-5"
		keyName = "TEST_ANTHROPIC_API_KEY"
	}
	if *authHome != "" {
		expectedModel = "gpt-6.1-sol"
	}
	if *model == "" {
		*model = expectedModel
	}
	if *model != expectedModel || (*auth == "api" && (*budget <= 0 || *budget > 1)) {
		fmt.Fprintln(os.Stderr, "use the pinned harness model and a budget above zero and at most $1")
		os.Exit(1)
	}
	if *candidateSource != "" && (*candidate == "" || (*localCodex == "" && *localClaude == "")) {
		fmt.Fprintln(os.Stderr, "--candidate-source requires a local agent and --candidate-release")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(signalCtx, 15*time.Minute)
	defer cancel()
	var captured *snapshotMetadata
	if *snapshotInput != "" {
		data, err := os.ReadFile(*snapshotInput)
		var m snapshotMetadata
		if err != nil || json.Unmarshal(data, &m) != nil || m.Schema != fingerprintSchema {
			fmt.Fprintln(os.Stderr, "invalid captured input manifest")
			os.Exit(1)
		}
		captured = &m
	}
	c := config{ToolingSource: *toolingSource, NodeSource: *nodeSource, SkillsSource: *skillsSource, SkillsCLIVersion: *skillsVersion, CodexAuthHome: *authHome, AgentVersion: *version, Harness: *harness, ClaudePermissionMode: *claudeMode, AuthMode: *auth, APIKey: os.Getenv(keyName), Model: *model, Budget: *budget, Scenario: *scenario, CandidateVersion: *candidate, CandidateSource: *candidateSource}
	if *auth == "subscription" {
		c.APIKey = ""
		if *harness == "claude" {
			c.OAuthToken = os.Getenv("TEST_CLAUDE_CODE_OAUTH_TOKEN")
		}
	}
	cache, hit := openScenarioCache(ctx, captured, *noCache, *scenario, *output)
	if hit {
		return
	}
	if *cacheOnly {
		fmt.Fprintln(os.Stderr, "No verified cached result; agent execution disabled")
		os.Exit(1)
	}
	var r report
	if *localClaude != "" {
		if *harness != "claude" {
			fmt.Fprintln(os.Stderr, "--local-claude requires --harness claude")
			os.Exit(1)
		}
		*localCodex = *localClaude
	}
	if *localCodex != "" {
		c.Codex = *localCodex
		r = runLocal(ctx, c, *localRoot)
	} else {
		fmt.Fprintln(os.Stderr, "onboarding E2E requires a local pinned agent executable (CI runs locally in its disposable runner)")
		os.Exit(1)
	}
	if captured != nil {
		r.Cache = &cacheProvenance{Fingerprint: captured.fingerprint(*scenario)}
	}
	r.Harness = c.Harness
	r.AuthMode = authenticationMode(c)
	data, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(*output, append(data, '\n'), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := writeReplay(*output+".html", r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if ctx.Err() == nil {
		cache.publish(*scenario, *output)
	}
	cleaned := r.Deleted
	if r.Execution == "local" {
		cleaned = r.LocalCleaned
	}
	if r.AuthMode == "subscription" {
		fmt.Printf("passed=%t auth=subscription billing=subscription_allowance cleanup_complete=%t report=%s\n", r.Passed, cleaned, *output)
	} else {
		fmt.Printf("passed=%t cost=$%.6f reserved=$%.6f cleanup_complete=%t report=%s\n", r.Passed, r.Usage.Cost, r.Usage.Reserved, cleaned, *output)
	}
	if !r.Passed || (r.VM != "" && !r.Deleted) || (r.Execution == "local" && !r.LocalCleaned) {
		fmt.Fprintln(os.Stderr, redact(redact(r.Error, c.APIKey), c.OAuthToken))
		os.Exit(1)
	}
}
