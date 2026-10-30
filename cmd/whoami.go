package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

type identityInfo struct {
	Profile   string                        `json:"profile"`
	Service   string                        `json:"service"`
	Status    string                        `json:"status"`
	Account   *adversarylabs.WhoamiResponse `json:"account,omitempty"`
	ExpiresAt string                        `json:"expires_at,omitempty"`
}

func newWhoamiCommand(app *application.App, api, profile *string) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use: "whoami", Short: "Show your account and selected connection profile",
		Example: "  doomer whoami\n  doomer whoami --profile anonymous-local\n  doomer whoami --profile work --format json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := validateFormat(format, false); err != nil {
				return err
			}
			deps := app.Dependencies()
			key := adversarylabs.AuthKey(*api, *profile)
			auth, exists, err := deps.Auth.StoredAuthE(key)
			if err != nil {
				return err
			}
			if !exists && key == adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default") {
				auth, exists, err = deps.Auth.StoredAuthE(deps.RegistryHost)
				if err != nil {
					return err
				}
			}
			info := identityInfo{Profile: *profile, Service: *api, Status: "anonymous"}
			if exists && strings.TrimSpace(auth.Token) != "" && !adversarylabs.IsAnonymousToken(auth.Token) {
				if auth.ExpiresAt != "" {
					expiry, e := time.Parse(time.RFC3339, auth.ExpiresAt)
					if e != nil {
						return fmt.Errorf("profile %q has an invalid login expiration; log in again", *profile)
					}
					if !time.Now().Before(expiry) {
						return fmt.Errorf("login for profile %q has expired; run doomer login --profile %s", *profile, *profile)
					}
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
				defer cancel()
				client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *api)
				account, e := client.Whoami(ctx, auth.Token)
				if e != nil {
					return fmt.Errorf("could not verify account for profile %q at %s: %w", *profile, *api, e)
				}
				info.Status, info.Account, info.ExpiresAt = "authenticated", &account, auth.ExpiresAt
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), "whoami", info)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Profile: %s\nService: %s\n", info.Profile, info.Service)
			if info.Account == nil {
				_, err = fmt.Fprintln(out, "You're using Doomer anonymously.\nLook Closer is available; log in to use Deep review.")
				return err
			}
			account := info.Account
			email := account.Email
			if email == "" {
				email = account.EmailAddress
			}
			name := account.Name
			if email != "" {
				if name != "" {
					name += " <" + email + ">"
				} else {
					name = email
				}
			}
			if name == "" {
				name = account.ID
			}
			if name == "" {
				name = "your account"
			}
			fmt.Fprintf(out, "Signed in as %s.\n", name)
			if account.Type != "" {
				fmt.Fprintf(out, "Account type: %s\n", strings.ReplaceAll(account.Type, "_", " "))
			}
			team := account.Team.Name
			if team == "" {
				team = account.Team.Slug
			}
			if team != "" {
				fmt.Fprintf(out, "Team: %s\n", team)
			}
			plan := account.Subscription.Plan
			if plan == "" {
				plan = account.Subscription.Name
			}
			if plan != "" {
				if account.Subscription.Status != "" {
					plan += " (" + account.Subscription.Status + ")"
				}
				fmt.Fprintf(out, "Plan: %s\n", plan)
			}
			if info.ExpiresAt != "" {
				fmt.Fprintf(out, "Login expires: %s\n", info.ExpiresAt)
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return command
}
