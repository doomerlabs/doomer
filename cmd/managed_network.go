package cmd

import (
	"errors"
	"fmt"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

const networkAccessDocsURL = "https://github.com/doomerlabs/doomer/blob/main/docs/network-access.md"

type managedNetworkAccess struct {
	RequiredHost string `json:"required_host"`
	SettingsURL  string `json:"settings_url,omitempty"`
	DocsURL      string `json:"docs_url"`
	RetryCommand string `json:"retry_command"`
}

func addManagedNetworkHelp(c *cobra.Command, r *managedResult, t managed.Task, err error) {
	var connection *managed.ConnectionError
	if !errors.As(err, &connection) {
		return
	}
	retry := "prepare --task " + watch.Quote(t.Handle)
	switch c.Name() {
	case "submit", "status", "finish":
		retry = "task " + c.Name() + " " + watch.Quote(t.Handle)
	case "retry":
		retry = "task submit " + watch.Quote(t.Handle)
	case "respond":
		retry = "task status " + watch.Quote(t.Handle)
	}
	retry = "doomer --api-url " + watch.Quote(t.API) + " --profile " + watch.Quote(t.Profile) + " " + retry
	r.NetworkAccess = &managedNetworkAccess{
		RequiredHost: connection.Host,
		DocsURL:      networkAccessDocsURL,
		RetryCommand: retry,
	}
	// Portable setup requests another grant and cannot repair network access.
	r.PortableSetupCommand = nil
	if t.Agent == "codex" {
		r.Action = fmt.Sprintf("Cannot reach %s. "+
			"Read the non-secret saved scope with doomer --api-url %s --profile %s preflight --agent codex --repo %s. "+
			"Retry %s through Codex's normal tool network permissions. "+
			"Reuse existing grants and preserve this task and setup mode. Honor explicit denials. "+
			"If the permitted retry fails, report %s and %s once and retain the task.",
			connection.Host, watch.Quote(t.API), watch.Quote(t.Profile), watch.Quote(t.Repo),
			r.NetworkAccess.RetryCommand, connection.Host, networkAccessDocsURL)
		return
	}
	r.NetworkAccess.SettingsURL = "https://claude.ai/settings"
	r.Action = fmt.Sprintf("Cannot reach %s. This may be a network restriction or a service/connectivity problem. "+
		"Show the user this exact hostname for copying and the setup guide %s. "+
		"For Claude, offer to open %s, then guide an organization owner to Organization settings, "+
		"the network access or domain allowlist section, and add only %s if they approve. "+
		"Labels can vary between Code and Capabilities. Personal accounts use their own settings; "+
		"Claude Code cloud sessions use their environment's Network access settings. "+
		"Explain why Doomer needs the connection. Let the user make the settings change; "+
		"never change it automatically or treat network access as repository upload consent. "+
		"After the user confirms, check again with %s. Keep this task handle. "+
		"If the same request is still blocked, stop retrying and ask the user to check the active organization/environment. "+
		"See the guide for existing sessions and later review-operation retries.",
		connection.Host, networkAccessDocsURL, r.NetworkAccess.SettingsURL, connection.Host, r.NetworkAccess.RetryCommand)
}

func outputManagedNetworkError(c *cobra.Command, s managed.Store, t managed.Task, err error) error {
	r := resultFor(t)
	addManagedNetworkHelp(c, &r, t, err)
	if r.NetworkAccess == nil {
		return err
	}
	r.State = "network_unavailable"
	r.Error = err.Error()
	return outputManaged(c, s, t, r)
}
