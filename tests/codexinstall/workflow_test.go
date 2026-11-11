package main

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestSelectorArtifactSurvivesDownstreamRetry(t *testing.T) {
	data, err := os.ReadFile("../../.depot/workflows/agent-e2e.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string         `yaml:"uses"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var produced, consumed string
	var overwrite bool
	for _, step := range workflow.Jobs["selector-shadow"].Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			produced, _ = step.With["name"].(string)
			overwrite, _ = step.With["overwrite"].(bool)
		}
	}
	for _, step := range workflow.Jobs["selector-comparison"].Steps {
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") && step.With["path"] == "e2e-selection" {
			consumed, _ = step.With["name"].(string)
		}
	}
	// Failed-job retries reuse a successful producer from attempt 1 while
	// running its downstream consumer in attempt 2.
	produced = strings.ReplaceAll(produced, "${{ github.run_attempt }}", "1")
	consumed = strings.ReplaceAll(consumed, "${{ github.run_attempt }}", "2")
	if produced == "" || consumed == "" || produced != consumed {
		t.Fatalf("downstream retry cannot find prediction artifact: produced %q, requested %q", produced, consumed)
	}
	if !overwrite {
		t.Fatal("a full rerun must be able to replace the prediction artifact")
	}
}
