package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

func TestCodexPolicyMigrationPreservesConfiguration(t *testing.T) {
	consent := managedConsentEvidence{Account: "one", Agent: "codex", AllRepositories: true, Service: "https://doomer.ai/api", Profile: "default"}
	legacy := consent
	legacy.Agent, legacy.AllRepositories, legacy.Repository = "", false, "/old/repo/.git"
	other := legacy
	other.Account = "other"
	oldPolicy := "Keep my rules\n" + proposedCodexReviewPolicy(legacy).ExtraPolicy + proposedCodexReviewPolicy(other).ExtraPolicy + "Keep the footer\n"
	old, err := patchCodexExtraPolicy([]byte("# header\nmodel = 'example'\n[auto_review] # keep\n# rules\nextra_policy = '' # trailing\n[other]\nenabled = true\n"), oldPolicy)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	path, _ := codexConfigPath()
	if err := os.WriteFile(path, old, 0640); err != nil {
		t.Fatal(err)
	}
	status, _, err := inspectCodexReviewPolicy(consent)
	if err != nil || status.State != "migration_required" {
		t.Fatal(status, err)
	}
	if err := installCodexReviewPolicy(consent); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(path)
	policy, err := codexExtraPolicy(updated)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(policy, proposedCodexReviewPolicy(legacy).ExtraPolicy) || !strings.Contains(policy, proposedCodexReviewPolicy(other).ExtraPolicy) || !strings.Contains(policy, "Keep my rules\n") || !strings.Contains(policy, "Keep the footer\n") {
		t.Fatal("unrelated policy changed or legacy scope retained", policy)
	}
	for _, s := range []string{"# header\nmodel = 'example'", "[auto_review] # keep\n# rules\n", " # trailing\n[other]\nenabled = true\n"} {
		if !strings.Contains(string(updated), s) {
			t.Fatal("configuration formatting changed", string(updated))
		}
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0640 {
		t.Fatal(info.Mode())
	}
	backups, _ := filepath.Glob(path + ".before-doomer-*")
	if len(backups) != 1 {
		t.Fatal(backups)
	}
	backup, _ := os.ReadFile(backups[0])
	if string(backup) != string(old) {
		t.Fatal("backup does not match original")
	}
	if err := installCodexReviewPolicy(consent); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(updated) {
		t.Fatal("migration was not idempotent")
	}
	status, _, err = inspectCodexReviewPolicy(consent)
	if err != nil || status.State != "configured" || status.Activation != "unverified" {
		t.Fatal(status, err)
	}
}

func TestCodexPolicyPatchLayouts(t *testing.T) {
	for _, input := range []string{"", "model='x'\n", "[auto_review]", "[auto_review]\nother=true\n[next]\nx=1\n", "auto_review.extra_policy='old' # keep\n", "[auto_review]\nextra_policy=\"\"\"\nold\n\"\"\" # keep\n"} {
		t.Run(input, func(t *testing.T) {
			out, err := patchCodexExtraPolicy([]byte(input), "new\npolicy")
			if err != nil {
				t.Fatal(err)
			}
			got, err := codexExtraPolicy(out)
			if err != nil || got != "new\npolicy" {
				t.Fatal(string(out), err)
			}
		})
	}
}

func TestCodexPolicyMigrationRejectsInvalidConfig(t *testing.T) {
	for _, input := range []string{"[broken", "[auto_review]\nextra_policy=123", "auto_review={extra_policy='old'}\n"} {
		t.Run(input, func(t *testing.T) {
			t.Setenv("CODEX_HOME", t.TempDir())
			path, _ := codexConfigPath()
			os.WriteFile(path, []byte(input), 0600)
			if err := installCodexReviewPolicy(managedConsentEvidence{Account: "one"}); err == nil {
				t.Fatal("invalid or unsupported configuration accepted")
			}
			after, _ := os.ReadFile(path)
			if string(after) != input {
				t.Fatal("configuration changed")
			}
		})
	}
}

func TestCodexPolicyOfferMigratesOldMarkersAndTracksActiveProfile(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	s := managed.Store{Root: t.TempDir()}
	consent := managedConsentEvidence{Account: "one", Agent: "codex", AllRepositories: true, Service: "https://doomer.ai/api", Profile: "default"}
	// Older versions keyed only on proposal text; delivery never meant installed.
	oldPath := filepath.Join(s.Root, "codex-policy-offers", watch.Key(proposedCodexReviewPolicy(consent).ExtraPolicy))
	os.MkdirAll(filepath.Dir(oldPath), 0700)
	os.WriteFile(oldPath, []byte("delivered\n"), 0600)
	p, err := offerCodexReviewPolicy(s, consent)
	if err != nil || p == nil || p.State != "missing" {
		t.Fatal(p, err)
	}
	if err := markCodexReviewPolicyOffered(s, p); err != nil {
		t.Fatal(err)
	}
	if p, err := offerCodexReviewPolicy(s, consent); err != nil || p != nil {
		t.Fatal(p, err)
	}
	// The same process history must not suppress migration in a different profile.
	t.Setenv("CODEX_HOME", t.TempDir())
	if p, err := offerCodexReviewPolicy(s, consent); err != nil || p == nil {
		t.Fatal(p, err)
	}
	if err := installCodexReviewPolicy(consent); err != nil {
		t.Fatal(err)
	}
	if p, err := offerCodexReviewPolicy(s, consent); err != nil || p != nil {
		t.Fatal("installed policy offered again", p, err)
	}
	path, _ := codexConfigPath()
	os.WriteFile(path, []byte("# removed by user\n"), 0600)
	if p, err := offerCodexReviewPolicy(s, consent); err != nil || p == nil {
		t.Fatal("removed policy was treated as installed", p, err)
	}
}
