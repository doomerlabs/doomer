package cmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/paths"
	"github.com/doomerlabs/doomer/internal/snapshot"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

type submission struct {
	Key      string          `json:"key"`
	Project  string          `json:"project"`
	APIURL   string          `json:"apiUrl"`
	Profile  string          `json:"profile"`
	ID       string          `json:"id,omitempty"`
	Digest   string          `json:"digest"`
	Snapshot json.RawMessage `json:"snapshot"`
}

func reviewClient(app *application.App, apiURL, profile string) (adversarylabs.Client, string, error) {
	auth, ok, err := app.Dependencies().Auth.StoredAuthE(adversarylabs.AuthKey(apiURL, profile))
	if err != nil {
		return adversarylabs.Client{}, "", err
	}
	if !ok || auth.Token == "" {
		return adversarylabs.Client{}, "", fmt.Errorf("login to the selected profile first")
	}
	return adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, apiURL), auth.Token, nil
}
func newRunCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	var project, root, base, resume string
	var staged, async bool
	cmd := &cobra.Command{Use: "run", Short: "Capture local changes and submit a hosted review", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		client, token, err := reviewClient(app, *apiURL, *profile)
		if err != nil {
			return err
		}
		dir, err := paths.DataDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(dir, "submissions")
		var s submission
		var local string
		if resume != "" {
			if strings.ContainsAny(resume, "/\\") || resume == "." || resume == ".." {
				return fmt.Errorf("invalid submission key")
			}
			local = filepath.Join(dir, resume+".json")
			b, e := os.ReadFile(local)
			if e != nil {
				return e
			}
			if e = json.Unmarshal(b, &s); e != nil {
				return e
			}
			if s.APIURL != *apiURL || s.Profile != *profile {
				return fmt.Errorf("resume requires the original API and profile")
			}
			if project != "" && project != s.Project {
				return fmt.Errorf("resume requires the original project")
			}
			if _, e = snapshot.Decode(s.Snapshot); e != nil {
				return e
			}
			if snapshot.Digest(s.Snapshot) != s.Digest {
				return fmt.Errorf("saved snapshot digest mismatch")
			}
		} else {
			if project == "" {
				return fmt.Errorf("--project is required")
			}
			b, e := snapshot.Capture(cmd.Context(), snapshot.Options{Path: root, Base: base, Staged: staged})
			if e != nil {
				return e
			}
			s = submission{Key: rand.Text(), Project: project, APIURL: *apiURL, Profile: *profile, Digest: snapshot.Digest(b), Snapshot: b}
			local = filepath.Join(dir, s.Key+".json")
			if err = os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			if err = saveSubmission(local, s); err != nil {
				return err
			}
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Snapshot %s captured. Resume interrupted submission with: doomer run --resume %s\n", s.Digest, s.Key)
		if s.ID == "" {
			r, e := client.CreateReview(cmd.Context(), token, s.Project, s.Key, s.Digest)
			if e != nil {
				return e
			}
			if e = r.Validate(); e != nil {
				return e
			}
			s.ID = r.ID
			if e = saveSubmission(local, s); e != nil {
				return e
			}
		}
		if err = client.UploadReview(cmd.Context(), token, s.ID, s.Snapshot); err != nil {
			return err
		}
		r, err := client.FinalizeReview(cmd.Context(), token, s.ID)
		if err != nil {
			return err
		}
		if err = r.Validate(); err != nil {
			return err
		}
		// Acceptance is durable remotely; local source can now be discarded.
		if err = os.Remove(local); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "Review accepted; local snapshot cleanup failed.")
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Review %s: %s\n", r.ID, r.Status)
		if async {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
		}
		return waitReview(cmd, client, token, r.ID)
	}}
	cmd.Flags().StringVar(&project, "project", "", "project slug or ID")
	cmd.Flags().StringVar(&root, "path", ".", "repository to capture")
	cmd.Flags().StringVar(&base, "base", "", "compare with the merge base of this ref and HEAD")
	cmd.Flags().BoolVar(&staged, "staged", false, "capture the index instead of current files")
	cmd.Flags().BoolVar(&async, "async", false, "return after queue admission")
	cmd.Flags().StringVar(&resume, "resume", "", "resume a saved submission without recapturing source")
	cmd.MarkFlagsMutuallyExclusive("resume", "path")
	cmd.MarkFlagsMutuallyExclusive("resume", "base")
	cmd.MarkFlagsMutuallyExclusive("resume", "staged")
	return cmd
}
func saveSubmission(p string, s submission) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".submission-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, p)
}
func newReviewsCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	root := &cobra.Command{Use: "reviews", Short: "Read captured hosted reviews"}
	root.AddCommand(&cobra.Command{Use: "show ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, token, err := reviewClient(app, *apiURL, *profile)
		if err != nil {
			return err
		}
		r, err := c.Review(cmd.Context(), token, args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
	}})
	return root
}
func waitReview(cmd *cobra.Command, c adversarylabs.Client, token, id string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
	defer cancel()
	for {
		r, err := c.Review(ctx, token, id)
		if err != nil {
			return fmt.Errorf("review %s continues remotely; use reviews show: %w", id, err)
		}
		switch r.Status {
		case "completed", "failed", "cancelled":
			if err = json.NewEncoder(cmd.OutOrStdout()).Encode(r); err != nil {
				return err
			}
			if r.Status != "completed" {
				return fmt.Errorf("review %s: %s", id, r.Status)
			}
			return nil
		}
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("review %s continues remotely: %w", id, ctx.Err())
		case <-timer.C:
		}
	}
}
