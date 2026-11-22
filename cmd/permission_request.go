package cmd

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func requestInstallerCheckoutApproval(c *cobra.Command, home, directory, agent string) error {
	if agent != "claude" && agent != "codex" {
		return fmt.Errorf("agent must be codex or claude")
	}
	root, err := watching.Root(c.Context(), directory)
	if err != nil {
		return err
	}
	if home == "" {
		home, err = defaultManagerHome()
		if err != nil {
			return err
		}
	}
	file := filepath.Join(home, "state.json")
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("open personal Doomer installer state: %w", err)
	}
	if _, err := os.Stat(filepath.Join(home, "uninstalling.json")); !os.IsNotExist(err) {
		return fmt.Errorf("Doomer installer is unavailable")
	}
	before, err := readNPMSetupState(home)
	if err != nil {
		return err
	}
	state, err := before.consent()
	if err != nil {
		return err
	}
	environment, err := directoryWatchEnvironment(root)
	if err != nil {
		return err
	}
	explicitAPI, _ := c.Flags().GetString("api-url")
	api, err := agentHookAPI(explicitAPI, environment.get("DOOMER_API_URL"), state.API)
	if err != nil {
		return err
	}
	if _, err := agentHookConsent(state, api, agent, root); err == nil {
		return nil
	}
	in, terminal := c.InOrStdin().(*os.File)
	if !terminal || !term.IsTerminal(int(in.Fd())) {
		return fmt.Errorf("run this command in an interactive terminal to approve uploads")
	}
	output := c.ErrOrStderr()
	fmt.Fprintf(output, "Quick reviews can send source snapshots, task context, and findings with responses to %s.\nCheckout: %q\nAgent: %s\nAllow these uploads? Type yes to approve: ", api, root, agent)
	if !terminalSetupConsent(c.Context(), in) {
		fmt.Fprintln(output, "Upload approval was not saved.")
		return nil
	}
	return saveInstallerCheckoutApproval(home, before, state, api, agent, root)
}

func saveInstallerCheckoutApproval(home string, before npmSetupState, state agentHookState, api, agent, root string) error {
	unlock, err := npmSetupLock(filepath.Join(home, "state.json"))
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(home, "uninstalling.json")); !os.IsNotExist(err) {
		return fmt.Errorf("Doomer installer is unavailable")
	}
	if _, err := os.Stat(filepath.Join(home, "state.json")); err != nil {
		return fmt.Errorf("open personal Doomer installer state: %w", err)
	}
	current, err := readNPMSetupState(home)
	if err != nil {
		return err
	}
	if !equalNPMSetupState(current, before) {
		return fmt.Errorf("Doomer installer state changed during approval; retry in Doctor")
	}
	var grants []json.RawMessage
	if err := current.get("grants", &grants); err != nil {
		return err
	}
	grant, err := json.Marshal(agentUploadGrant{
		Scope: "repository", API: api, Repo: root, Agents: []string{agent}, ApprovedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	current.set("grants", append(grants, grant))
	if state.Identities[api] == "" {
		if state.Identities == nil {
			state.Identities = make(map[string]string)
		}
		var entropy [32]byte
		rand.Read(entropy[:])
		state.Identities[api] = "doomer_anon_" + base64.RawURLEncoding.EncodeToString(entropy[:])
		current.set("identities", state.Identities)
	}
	return writeNPMSetupState(home, current)
}
