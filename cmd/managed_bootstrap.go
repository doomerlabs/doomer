package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/version"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

func newManagedBootstrapCommand() *cobra.Command {
	root := &cobra.Command{Use: "bootstrap", Short: "Check and explicitly upgrade installed skill wrappers"}
	for _, operation := range []string{"status", "upgrade", "defer"} {
		operation := operation
		var repo string
		var personal, accept bool
		c := &cobra.Command{Use: operation, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
			base, err := os.UserHomeDir()
			scope := "personal"
			if err != nil {
				return err
			}
			if !personal {
				base, err = watch.Root(c.Context(), repo)
				scope = "project"
				if err != nil {
					return err
				}
			}
			paths := []string{filepath.Join(base, ".agents/skills/doomer-review/SKILL.md"), filepath.Join(base, ".claude/skills/doomer-review/SKILL.md")}
			var updates []managed.BootstrapUpdate
			var policy *managed.Policy
			expected := map[string][]byte{}
			for _, path := range paths {
				b, err := managed.ReadBootstrap(path, scope)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return err
				}
				if policy != nil && *policy != b.Policy {
					return fmt.Errorf("bootstrap policies disagree; reconcile them before upgrading")
				}
				p := b.Policy
				policy = &p
				update, err := managed.InspectBootstrap(path, scope, version.Version)
				if err != nil {
					return err
				}
				updates = append(updates, update)
				if operation == "upgrade" && accept {
					if update.State != "current" && update.State != "update_available" {
						return fmt.Errorf("cannot replace %s bootstrap at %s; reconcile it manually", update.State, path)
					}
					expected[path], err = os.ReadFile(path)
					if err != nil {
						return err
					}
					if !managed.RecognizedBootstrap(expected[path], b.Policy) {
						return fmt.Errorf("bootstrap changed during inspection; inspect it again")
					}
				}
			}
			if policy == nil {
				return fmt.Errorf("no installed bootstrap in selected scope")
			}
			state := "bootstrap_status"
			if operation == "defer" {
				identity, err := bootstrapDeferralIdentity(c.Context(), scope, base)
				if err != nil {
					return err
				}
				s, err := managedStore()
				if err != nil {
					return err
				}
				if err = s.DeferBootstrap(identity, managed.BootstrapVersion, time.Now()); err != nil {
					return err
				}
				state = "bootstrap_deferred"
			}
			if operation == "upgrade" && accept {
				content, err := managed.BootstrapText(*policy)
				if err != nil {
					return err
				}
				if err = writeBootstrapFiles(base, paths, []byte(content), expected); err != nil {
					return err
				}
				state = "bootstrap_upgraded"
				updates = nil
				for _, path := range paths {
					update, err := managed.InspectBootstrap(path, scope, version.Version)
					if err != nil {
						return err
					}
					updates = append(updates, update)
				}
			}
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"state": state, "bootstraps": updates, "acceptance_required": operation == "upgrade" && !accept})
		}}
		c.Flags().StringVar(&repo, "repo", ".", "repository to inspect")
		c.Flags().BoolVar(&personal, "personal", false, "inspect personal wrappers instead of repository wrappers")
		if operation == "upgrade" {
			c.Flags().BoolVar(&accept, "accept", false, "apply the offered wrapper upgrade after user acceptance")
		}
		root.AddCommand(c)
	}
	return root
}

func managedBootstrapUpdates(task managed.Task) []managed.BootstrapUpdate {
	if task.Bootstrap.Path == "" {
		return nil
	}
	base := filepath.Clean(filepath.Join(filepath.Dir(task.Bootstrap.Path), "../../.."))
	identity, identityErr := bootstrapDeferralIdentity(context.Background(), task.Bootstrap.Scope, base)
	store, storeErr := managedStoreLocation()
	deferred := identityErr == nil && storeErr == nil && store.BootstrapDeferred(identity, managed.BootstrapVersion, time.Now())
	var updates []managed.BootstrapUpdate
	for _, agent := range []string{".agents", ".claude"} {
		path := filepath.Join(base, agent, "skills/doomer-review/SKILL.md")
		update, err := managed.InspectBootstrap(path, task.Bootstrap.Scope, task.Release)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			updates = append(updates, managed.BootstrapUpdate{Path: path, State: "inspection_failed", Available: managed.BootstrapVersion, CLIRange: managed.BootstrapCLIRange, Action: "Could not inspect this wrapper. Run doomer bootstrap status in its installation scope and reconcile it manually. The current task can continue."})
			continue
		}
		if update.State == "current" {
			continue
		}
		if update.State == "update_available" {
			if deferred {
				continue
			}
			target := "--repo " + watch.Quote(base)
			if task.Bootstrap.Scope == "personal" {
				target = "--personal"
			}
			update.Action = managed.BootstrapUpgradeOffer(task.Bootstrap.Scope) + " Only after acceptance run doomer bootstrap upgrade --accept " + target + ". If the user chooses Maybe later, run doomer bootstrap defer " + target + " to suppress this version's offer for 24 hours. The task keeps its release and update policy."
		}
		updates = append(updates, update)
	}
	return updates
}

func bootstrapDeferralIdentity(ctx context.Context, scope, base string) (string, error) {
	if scope == "personal" {
		return "personal:" + base, nil
	}
	identity, err := watch.RepositoryIdentity(ctx, base)
	return "project:" + identity, err
}
