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
const BootstrapVersion = "2026.11.6.13"
const BootstrapCLIRange = ">=2026.11.6.9"

//go:embed bootstrap-legacy.md
var legacyBootstrapTemplate string

//go:embed bootstrap-2026.11.6.10.md
var previousVersionedBootstrapTemplate string

//go:embed bootstrap-2026.11.6.11.md
var versionElevenBootstrapTemplate string

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

func BootstrapUpgradeOffer(scope string) string {
	location := "this repository’s"
	if scope == "personal" {
		location = "your personal"
	}
	offer := "Ask the user: ‘A new version of the Doomer skill is available. Update " + location + " skill files now? Your current version still works.’ Present choices in this order: ‘Update skill (recommended)’, ‘Maybe later’."
	if scope == "personal" {
		return offer + " Apply an accepted personal update locally without creating a pull request."
	}
	return offer + " Before applying an accepted update, check which skill files exist in Git HEAD. For committed skill files, make the update on a separate branch and open a pull request containing only those skill updates. Preserve unrelated changes and any existing local skill edits. If a changed skill file is untracked, update it locally and leave it untracked. If no changed skill files are committed, do not create a pull request. If PR creation is unavailable, report that the local update still needs a pull request."
}

func RecognizedBootstrap(data []byte, policy Policy) bool {
	current, err := BootstrapText(policy)
	if err != nil {
		return false
	}
	return string(data) == current || recognizedPreviousBootstrap(data, policy)
}

func recognizedPreviousBootstrap(data []byte, policy Policy) bool {
	for _, previous := range []struct{ template, revision, cliRange string }{
		{legacyBootstrapTemplate, "", ""},
		{previousVersionedBootstrapTemplate, "2026.11.6.10", ">=2026.11.6.9"},
		{versionElevenBootstrapTemplate, "2026.11.6.11", ">=2026.11.6.9"},
	} {
		text, err := bootstrapText(policy, previous.template, previous.revision, previous.cliRange)
		if err == nil && string(data) == text {
			return true
		}
	}
	return false
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
	result.State = "modified"
	result.Action = "The wrapper differs from the bundled templates. Review its local changes and reconcile manually."
	if recognizedPreviousBootstrap(data, b.Policy) {
		result.State = "update_available"
		result.Action = BootstrapUpgradeOffer(scope) + " After acceptance, run doomer bootstrap upgrade with the same scope. Preserve the update policy."
	}
	return result, nil
}
