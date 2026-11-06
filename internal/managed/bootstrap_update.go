package managed

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
)

// Bump the wrapper revision only when its generated content changes, using the
// first Doomer release carrying that change. Ordinary CLI releases keep it.
const BootstrapVersion = "2026.11.6.11"
const BootstrapCLIRange = ">=2026.11.6.9"

//go:embed bootstrap-legacy.md
var legacyBootstrapTemplate string

// BootstrapSupports accepts whitespace-separated comparison clauses against
// Doomer's four-part release versions, e.g. ">=2026.11.6.9 <2027.1.1.1".
func BootstrapSupports(constraint, version string) (bool, error) {
	clauses := strings.Fields(constraint)
	if len(clauses) == 0 {
		return false, fmt.Errorf("empty doomer-cli-range")
	}
	compatible := runtimeversion.Release.MatchString(version)
	for _, clause := range clauses {
		op := "="
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(clause, candidate) {
				op = candidate
				clause = strings.TrimPrefix(clause, candidate)
				break
			}
		}
		if !runtimeversion.Release.MatchString(clause) {
			return false, fmt.Errorf("invalid doomer-cli-range comparison")
		}
		less, greater := VersionLess(version, clause), VersionLess(clause, version)
		match := false
		switch op {
		case ">=":
			match = !less
		case "<=":
			match = !greater
		case ">":
			match = greater
		case "<":
			match = less
		case "=":
			match = !less && !greater
		}
		compatible = compatible && match
	}
	return compatible, nil
}

type BootstrapUpdate struct {
	Path       string `json:"path"`
	State      string `json:"state"`
	Installed  string `json:"installed,omitempty"`
	Available  string `json:"available"`
	CLIRange   string `json:"cli_range"`
	Action     string `json:"action,omitempty"`
	Compatible *bool  `json:"compatible,omitempty"`
}

func RecognizedBootstrap(data []byte, policy Policy) bool {
	current, err := BootstrapText(policy)
	if err != nil {
		return false
	}
	legacy, err := bootstrapText(policy, legacyBootstrapTemplate, false)
	return err == nil && (string(data) == current || string(data) == legacy)
}

func InspectBootstrap(path, scope, version string) (BootstrapUpdate, error) {
	b, err := ReadBootstrap(path, scope)
	result := BootstrapUpdate{Path: path, Installed: b.Revision, Available: BootstrapVersion, CLIRange: BootstrapCLIRange}
	if err != nil {
		return result, err
	}
	if b.CLIRange != "" && runtimeversion.Release.MatchString(version) {
		supported, err := BootstrapSupports(b.CLIRange, version)
		if err != nil {
			return result, err
		}
		result.Compatible = &supported
		if !supported {
			result.State = "incompatible"
			result.CLIRange = b.CLIRange
			result.Action = "Use a Doomer release matching the wrapper's doomer-cli-range, or explicitly upgrade the wrapper."
			return result, nil
		}
	}
	current, err := BootstrapText(b.Policy)
	if err != nil {
		return result, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}
	if string(data) == current {
		result.State = "current"
		return result, nil
	}
	if b.Revision != "" && VersionLess(BootstrapVersion, b.Revision) {
		result.State = "newer"
		return result, nil
	}
	legacy, err := bootstrapText(b.Policy, legacyBootstrapTemplate, false)
	if err != nil {
		return result, err
	}
	result.State = "modified"
	result.Action = "The wrapper differs from the bundled templates. Review its local changes and reconcile manually."
	if string(data) == legacy {
		result.State = "update_available"
		result.Action = "Offer the user a bootstrap upgrade. After acceptance, run doomer bootstrap upgrade with the same scope. Preserve the update policy."
	}
	return result, nil
}
