package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type suiteCase struct {
	Scenario string   `json:"scenario"`
	Passed   bool     `json:"passed"`
	Report   string   `json:"report"`
	Charged  *float64 `json:"budget_charged_usd,omitempty"`
}

func runSuite(output string, perRun, totalLimit float64, auth string) error {
	if auth == "api" && (perRun <= 0 || perRun > 1 || totalLimit <= 0) {
		return fmt.Errorf("invalid suite budget")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	for _, scenario := range scenarioNames {
		path := strings.TrimSuffix(output, filepath.Ext(output)) + "-" + scenario + ".json"
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("existing report %s; choose a new output prefix", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	spent := 0.0
	var results []suiteCase
	failed := false
	for _, scenario := range scenarioNames {
		if auth == "api" && spent+perRun > totalLimit {
			return fmt.Errorf("suite stopped before %s: remaining budget cannot cover its reservation", scenario)
		}
		path := strings.TrimSuffix(output, filepath.Ext(output)) + "-" + scenario + ".json"
		// Append overrides so the original harness, model and execution flags survive.
		args := append(append([]string{}, os.Args[1:]...), "--scenario", scenario, "--output", path)
		command := exec.Command(executable, args...)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		runErr := command.Run()
		charged := perRun
		var report report
		data, readErr := os.ReadFile(path)
		if readErr == nil && json.Unmarshal(data, &report) == nil {
			charged = report.Usage.Reserved
		}
		passed := runErr == nil && report.Passed
		if !passed {
			failed = true
		}
		var runCharge *float64
		if auth == "api" {
			spent += charged
			runCharge = &charged
		}
		results = append(results, suiteCase{scenario, passed, path, runCharge})
		summary := struct {
			AuthMode string   `json:"auth_mode"`
			Charged  *float64 `json:"budget_charged_usd,omitempty"`
			Results  any      `json:"results"`
		}{AuthMode: auth, Results: results}
		if auth == "api" {
			summary.Charged = &spent
		}
		encoded, _ := json.MarshalIndent(summary, "", "  ")
		if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
			return err
		}
		if auth == "subscription" && !passed {
			return fmt.Errorf("subscription suite stopped after %s failed; see %s", scenario, output)
		}
	}
	if failed {
		return fmt.Errorf("one or more lifecycle scenarios failed; see %s", output)
	}
	return nil
}
