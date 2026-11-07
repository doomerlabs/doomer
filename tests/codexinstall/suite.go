package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runSuite(output string, perRun, totalLimit float64) error {
	if perRun <= 0 || perRun > 1 || totalLimit <= 0 {
		return fmt.Errorf("invalid suite budget")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	spent := 0.0
	var results []struct {
		Scenario string  `json:"scenario"`
		Passed   bool    `json:"passed"`
		Report   string  `json:"report"`
		Charged  float64 `json:"budget_charged_usd"`
	}
	failed := false
	for _, scenario := range scenarioNames {
		if spent+perRun > totalLimit {
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
		spent += charged
		results = append(results, struct {
			Scenario string  `json:"scenario"`
			Passed   bool    `json:"passed"`
			Report   string  `json:"report"`
			Charged  float64 `json:"budget_charged_usd"`
		}{scenario, passed, path, charged})
		summary := struct {
			Charged float64 `json:"budget_charged_usd"`
			Results any     `json:"results"`
		}{spent, results}
		encoded, _ := json.MarshalIndent(summary, "", "  ")
		if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
			return err
		}
	}
	if failed {
		return fmt.Errorf("one or more lifecycle scenarios failed; see %s", output)
	}
	return nil
}
