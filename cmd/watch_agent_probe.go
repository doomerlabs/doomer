package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/internal/doctor"
	"github.com/doomerlabs/doomer/internal/livewatch"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

func runAgentSessionProbe(c *cobra.Command, home, agent, directory string) map[string]any {
	ctx, cancel := context.WithTimeout(c.Context(), 25*time.Second)
	defer cancel()
	result := doctor.SessionCheck("checkout_unavailable")
	var checkout string
	if directory, err := filepath.EvalSymlinks(directory); err == nil && filepath.IsAbs(directory) {
		if path, err := livewatch.Root(ctx, directory); err == nil {
			checkout = path
			result = probeWatchStartup(ctx, c, home, agent, directory)
		}
	}
	details := result.String() + "\nRetry with the same prompt after making a fix."
	if checkout != "" {
		details += "\nCheckout: " + watching.Quote(doctor.RedactDescription(checkout))
	}
	return map[string]any{
		"systemMessage": result.Label,
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": details,
		},
	}
}

func probeWatchStartup(ctx context.Context, c *cobra.Command, home, agent, directory string) doctor.SessionResult {
	result := doctor.SessionCheck("")
	exe, err := turnExecutable()
	if err != nil {
		return result
	}
	data, err := json.Marshal(watchConfiguration{
		Config:      livewatch.Config{ID: rand.Text(), Directory: directory, Agent: agent},
		ManagerHome: home,
		ProbeOnly:   true,
	})
	if err != nil {
		return result
	}
	child := exec.CommandContext(ctx, exe, agentWatchArguments(c)...)
	child.Stdin = bytes.NewReader(data)
	output, err := child.Output()
	if err == nil {
		var receipt struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(output, &receipt) == nil {
			return doctor.SessionCheck(receipt.Code)
		}
	}
	return result
}

func agentWatchArguments(c *cobra.Command) []string {
	arguments := []string{"watch", "--config-stdin"}
	for _, name := range []string{"api-url", "profile"} {
		if c.Flags().Changed(name) || c.InheritedFlags().Changed(name) {
			value, _ := c.Flags().GetString(name)
			arguments = append(arguments, "--"+name, value)
		}
	}
	return arguments
}
