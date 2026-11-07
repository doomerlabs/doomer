package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
)

// Candidate injection is restricted to this disposable fixture home. It lets
// real-agent tests exercise unreleased fixes after verifying public installation.
func installCandidate(c config, home string) error {
	data, err := os.ReadFile(c.CandidateFile)
	if err != nil {
		return err
	}
	path := runtimeversion.Binary(home, c.CandidateVersion)
	recordPath := filepath.Join(filepath.Dir(path), "installation.json")
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		return fmt.Errorf("candidate release must match the installed public release: %w", err)
	}
	var rec selfupdate.Record
	if err = json.Unmarshal(raw, &rec); err != nil {
		return err
	}
	if rec.Version != c.CandidateVersion {
		return fmt.Errorf("candidate version mismatch")
	}
	if err = os.WriteFile(path, data, 0700); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	rec.SHA256 = hex.EncodeToString(sum[:])
	raw, err = json.Marshal(rec)
	if err != nil {
		return err
	}
	if err = os.WriteFile(recordPath, raw, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, ".local/bin/doomer"), data, 0700)
}
