package cmd

import (
	"fmt"
	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type loginOptions struct {
	ci                                         bool
	name, emailAddress, registryNamespace      string
	passwordStdin, tokenStdin, device, replace bool
}
type logoutOptions struct{ localOnly bool }

func newLoginCommand(app *application.App, apiURL, profile *string, settings *viper.Viper) *cobra.Command {
	opts := &loginOptions{}
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with Doomer",
		Example: `  doomer login
  doomer login --name "Marc's MacBook Pro"
  doomer login --ci
  printf '%s\n' "$DOOMER_TOKEN" | doomer login --token-stdin --registry-namespace my-team
  doomer login --email-address marc@example.com
  printf '%s\n' "$DOOMER_PASSWORD" | doomer login --email-address marc@example.com --password-stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.device && (opts.emailAddress != "" || opts.passwordStdin) {
				return fmt.Errorf("--device cannot be combined with password login")
			}
			deps := app.Dependencies()
			stdin, clock, browserAuth, tty := cmd.InOrStdin(), deps.Clock, deps.BrowserAuth, deps.TTY
			store := deps.Auth
			var err error
			targetProfile, newSettings, err := loginProfile(deps, valueOf(apiURL), valueOf(profile), settings, opts.replace)
			if err != nil {
				return err
			}
			client := deps.API.New(valueOf(apiURL))
			var token adversarylabs.TokenResponse
			if opts.tokenStdin {
				if opts.emailAddress != "" || opts.passwordStdin || opts.device || opts.ci {
					return fmt.Errorf("--token-stdin cannot be combined with password, device, or CI login options")
				}
				value, readErr := readSecretLine(stdin, "token")
				if readErr != nil {
					return readErr
				}
				token = adversarylabs.TokenResponse{Token: value, RegistryNamespace: opts.registryNamespace}
			} else if opts.emailAddress != "" || opts.passwordStdin {
				if opts.registryNamespace != "" {
					return fmt.Errorf("--registry-namespace requires --token-stdin")
				}
				if opts.emailAddress == "" {
					return fmt.Errorf("--email-address is required when --password-stdin is provided")
				}
				var password string
				if opts.passwordStdin {
					password, err = readPasswordLine(stdin)
				} else {
					secret, secretErr := tty.ReadSecret(cmd.Context(), stdin, cmd.ErrOrStderr())
					err = secretErr
					password = string(secret)
					for i := range secret {
						secret[i] = 0
					}
					if err != nil {
						return err
					}
				}
				token, err = client.LoginWithPassword(cmd.Context(), adversarylabs.PasswordLoginOptions{
					EmailAddress: opts.emailAddress,
					Password:     password,
					Name:         opts.name,
					CI:           opts.ci,
				})
				if err != nil {
					return err
				}
			} else if opts.ci || opts.device {
				if opts.registryNamespace != "" {
					return fmt.Errorf("--registry-namespace requires --token-stdin")
				}
				token, err = loginWithDevice(cmd.Context(), clock, cmd.OutOrStdout(), client, opts)
				if err != nil {
					return err
				}
			} else {
				if opts.registryNamespace != "" {
					return fmt.Errorf("--registry-namespace requires --token-stdin")
				}
				token, err = browserAuth.Login(cmd.Context(), application.BrowserAuthRequest{Client: client, Name: opts.name, CI: opts.ci, Output: cmd.OutOrStdout()})
				if err != nil {
					return err
				}
			}
			if err := store.SetAuth(adversarylabs.AuthKey(valueOf(apiURL), targetProfile), adversarylabs.Auth{
				Token:             token.Token,
				ClientID:          token.ClientID,
				ExpiresAt:         token.ExpiresAt,
				RegistryNamespace: token.RegistryNamespace,
				Namespace:         token.Namespace,
				Team:              token.Team,
				RegistryHost:      deps.RegistryHost,
			}); err != nil {
				return err
			}
			if newSettings != nil {
				if err := writeSettings(newSettings); err != nil {
					return fmt.Errorf("login saved in profile %q, but could not select it: %w", targetProfile, err)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout())
			if newSettings != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Created and selected profile %s; preserved login in %s.\n", targetProfile, valueOf(profile))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logged in to Doomer using profile %s.\n", targetProfile)
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.ci, "ci", false, "request a short-lived automation token")
	cmd.Flags().BoolVar(&opts.device, "device", false, "use device login for a headless environment")
	cmd.Flags().BoolVar(&opts.replace, "replace", false, "replace the selected profile's existing login")
	cmd.Flags().StringVar(&opts.name, "name", "", "friendly name for this client")
	cmd.Flags().StringVar(&opts.emailAddress, "email-address", "", "email address for password login")
	cmd.Flags().BoolVar(&opts.passwordStdin, "password-stdin", false, "read the password from standard input")
	cmd.Flags().BoolVar(&opts.tokenStdin, "token-stdin", false, "read a service account or short-lived CI token from standard input")
	cmd.Flags().StringVar(&opts.registryNamespace, "registry-namespace", "", "registry namespace for a service account or CI token")
	return cmd
}

func newLogoutCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	opts := &logoutOptions{}
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Log out of Doomer",
		Example: `  doomer logout
  doomer logout --local-only`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			deps := app.Dependencies()
			store := deps.Auth
			key := adversarylabs.AuthKey(valueOf(apiURL), valueOf(profile))
			auth, ok, err := store.StoredAuthE(key)
			if err != nil {
				return err
			}
			if !ok && key == adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default") { // exact legacy migration fallback
				auth, ok, err = store.StoredAuthE(deps.RegistryHost)
				key = deps.RegistryHost
				if err != nil {
					return err
				}
			}
			if !ok {
				fmt.Fprintln(cmd.OutOrStdout(), "No Doomer login was configured.")
				return nil
			}
			if !opts.localOnly && auth.Token != "" {
				client := deps.API.New(valueOf(apiURL))
				if err := client.Revoke(cmd.Context(), auth.Token); err != nil {
					return fmt.Errorf("token revocation failed; local credentials preserved: %w", err)
				}
			}
			if err := store.RemoveAuthCAS(key, auth); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Logged out of Doomer.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.localOnly, "local-only", false, "remove local credentials without contacting Doomer")
	return cmd
}
