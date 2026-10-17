package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func newReviewGetCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use: "get <review-id>", Aliases: []string{"status"},
		Short: "Read a submitted review's status, findings, summary, conclusion, and errors",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := validateFormat(format, false); err != nil {
				return err
			}
			deps := app.Dependencies()
			auth, ok, err := scopedAuth(deps.Auth, *apiURL, *profile, deps.RegistryHost)
			if err != nil {
				return err
			}
			if !ok || strings.TrimSpace(auth.Token) == "" {
				return fmt.Errorf("not logged in for profile %q; run doomer --profile %s login", *profile, *profile)
			}
			client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *apiURL)
			review, err := client.GetReview(cmd.Context(), auth.Token, args[0])
			if err != nil {
				return err
			}
			return writeReviewStatus(cmd.OutOrStdout(), "review get", format, review)
		},
	}
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return command
}

func reviewText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func writeReviewStatus(w io.Writer, command, format string, review adversarylabs.ReviewStatus) error {
	if format == "json" {
		return writeJSON(w, command, review)
	}
	if _, err := fmt.Fprintf(w, "Review %s: %s\n", reviewText(review.ID), reviewText(review.Status)); err != nil {
		return err
	}
	if review.Result != nil {
		result := review.Result
		if _, err := fmt.Fprintf(w, "Conclusion: %s\nSummary: %s\nFindings: %d\n", reviewText(result.Conclusion), reviewText(result.Summary), len(result.Findings)); err != nil {
			return err
		}
		for _, finding := range result.Findings {
			var pretty strings.Builder
			// Raw JSON preserves the server's complete finding schema, including evidence.
			var value any
			if err := json.Unmarshal(finding, &value); err != nil {
				return err
			}
			encoded, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				return err
			}
			pretty.Write(encoded)
			if _, err := fmt.Fprintln(w, pretty.String()); err != nil {
				return err
			}
		}
	}
	if review.Error != "" {
		_, err := fmt.Fprintf(w, "Error: %s\n", reviewText(review.Error))
		return err
	}
	return nil
}
