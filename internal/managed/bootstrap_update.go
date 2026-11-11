package managed

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
)

// Bump the wrapper revision only when its generated content changes, using the
// next monotonic bootstrap revision. Bootstrap and CLI release sequences are
// independent; ordinary CLI releases keep the existing bootstrap revision.
const BootstrapVersion = "2026.11.10.3"

const personalBootstrapVersion = "2026.11.10.3"

func BootstrapVersionForScope(scope string) string {
	if scope == "personal" && VersionLess(BootstrapVersion, personalBootstrapVersion) {
		return personalBootstrapVersion
	}
	return BootstrapVersion
}

// A linker override lets the disposable real-agent candidate (which is labelled
// with the installed public release) advertise its actual injected compatibility.
// Published release builds always use this source-defined default.
var BootstrapCLIRange = ">=2026.11.10.1"

//go:embed bootstrap-2026.11.10.2.md
var versionTenTwoBootstrapTemplate string

//go:embed bootstrap-2026.11.10.1.md
var versionTenOneBootstrapTemplate string

//go:embed bootstrap-2026.11.9.6.md
var versionNineSixBootstrapTemplate string

//go:embed bootstrap-2026.11.9.4.md
var versionNineFourBootstrapTemplate string

//go:embed bootstrap-2026.11.9.5.md
var versionNineFivePersonalBootstrapTemplate string

//go:embed bootstrap-legacy.md
var legacyBootstrapTemplate string

//go:embed bootstrap-2026.11.6.10.md
var previousVersionedBootstrapTemplate string

//go:embed bootstrap-2026.11.6.11.md
var versionElevenBootstrapTemplate string

//go:embed bootstrap-2026.11.6.13.md
var versionThirteenBootstrapTemplate string

//go:embed bootstrap-2026.11.7.3.md
var versionSevenThreeBootstrapTemplate string

//go:embed bootstrap-2026.11.7.5.md
var versionSevenFiveBootstrapTemplate string

//go:embed bootstrap-2026.11.8.1.md
var versionEightOneBootstrapTemplate string

//go:embed bootstrap-2026.11.8.2.md
var versionEightTwoBootstrapTemplate string

//go:embed bootstrap-2026.11.8.3.md
var versionEightThreeBootstrapTemplate string

//go:embed bootstrap-2026.11.8.4.md
var versionEightFourBootstrapTemplate string

//go:embed bootstrap-2026.11.8.5.md
var versionEightFiveBootstrapTemplate string

//go:embed bootstrap-2026.11.8.6.md
var versionEightSixBootstrapTemplate string

//go:embed bootstrap-2026.11.8.7.md
var versionEightSevenBootstrapTemplate string

//go:embed bootstrap-2026.11.8.8.md
var versionEightEightBootstrapTemplate string

//go:embed bootstrap-2026.11.8.9.md
var versionEightNineBootstrapTemplate string

//go:embed bootstrap-2026.11.8.10.md
var versionEightTenBootstrapTemplate string

//go:embed bootstrap-2026.11.8.12.md
var versionEightTwelveBootstrapTemplate string

//go:embed bootstrap-2026.11.9.3.md
var versionNineThreeBootstrapTemplate string

//go:embed bootstrap-2026.11.9.2.md
var versionNineTwoBootstrapTemplate string

//go:embed bootstrap-2026.11.9.1.md
var versionNineOneBootstrapTemplate string

//go:embed upgrade-prompt-rules-legacy.md
var legacyUpgradePromptRules string

//go:embed final-review-rules-legacy.md
var legacyFinalReviewRules string

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
	Path                string   `json:"path"`
	Scope               string   `json:"scope"`
	UpgradeAvailable    bool     `json:"upgrade_available"`
	State               string   `json:"state"`
	Installed           string   `json:"installed,omitempty"`
	Available           string   `json:"available"`
	CLIRange            string   `json:"cli_range"`
	DeferCommand        []string `json:"defer_command,omitempty"`
	UpgradeCommand      []string `json:"upgrade_command,omitempty"`
	UpdateManager       string   `json:"update_manager,omitempty"`
	ManualUpdateCommand []string `json:"manual_update_command,omitempty"`
	Action              string   `json:"action,omitempty"`
	Compatible          *bool    `json:"compatible,omitempty"`
}

func BootstrapUpgradeOffer(scope string) string {
	return strings.TrimSpace(UpgradePromptRules) + "\n\n" + BootstrapUpgradeDecision(scope)
}

// BootstrapUpgradeDecision omits the shared rules already returned in runtime
// instructions, keeping each actionable update small enough to read in full.
func BootstrapUpgradeDecision(scope string) string {
	location := "this repository’s"
	workflow := ", using a skill-only pull request for committed files"
	if scope == "personal" {
		location = "your personal"
		workflow = ""
	}
	offer := "If the checks above permit an offer, ask the user: ‘A new version of the Doomer skill is available. Update " + location + " skill files now" + workflow + "? Your current version still works.’ Include the update workflow in the approval question. Present choices in this order: ‘Update skill (recommended)’, ‘Maybe later’."
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
	personal, err := BootstrapTextForScope(policy, "personal")
	return err == nil && (string(data) == current || string(data) == personal || recognizedPreviousBootstrap(data, policy))
}

func recognizedPreviousBootstrap(data []byte, policy Policy) bool {
	for _, previous := range []struct{ template, revision, cliRange string }{
		{legacyBootstrapTemplate, "", ""},
		{versionTenTwoBootstrapTemplate, "2026.11.10.2", ">=2026.11.10.1"},
		{versionTenOneBootstrapTemplate, "2026.11.10.1", ">=2026.11.10.1"},
		{personalBootstrapTemplate(versionTenOneBootstrapTemplate), "2026.11.10.1", ">=2026.11.10.1"},
		{versionNineSixBootstrapTemplate, "2026.11.9.6", ">=2026.11.9.6"},
		{personalBootstrapTemplate(versionNineSixBootstrapTemplate), "2026.11.9.6", ">=2026.11.9.6"},
		{versionNineFourBootstrapTemplate, "2026.11.9.4", ">=2026.11.9.4"},
		{versionNineFivePersonalBootstrapTemplate, "2026.11.9.5", ">=2026.11.9.4"},
		{versionNineThreeBootstrapTemplate, "2026.11.9.3", ">=2026.11.9.3"},
		{versionNineTwoBootstrapTemplate, "2026.11.9.2", ">=2026.11.9.2"},
		{versionNineOneBootstrapTemplate, "2026.11.9.1", ">=2026.11.9.1"},
		{versionEightTwelveBootstrapTemplate, "2026.11.8.12", ">=2026.11.8.7"},
		{versionEightTenBootstrapTemplate, "2026.11.8.10", ">=2026.11.7.5"},
		{versionEightNineBootstrapTemplate, "2026.11.8.9", ">=2026.11.7.5"},
		{versionEightEightBootstrapTemplate, "2026.11.8.8", ">=2026.11.7.5"},
		{versionEightSevenBootstrapTemplate, "2026.11.8.7", ">=2026.11.7.5"},
		{versionEightSixBootstrapTemplate, "2026.11.8.6", ">=2026.11.7.5"},
		{versionEightFiveBootstrapTemplate, "2026.11.8.5", ">=2026.11.7.5"},
		{versionEightTwoBootstrapTemplate, "2026.11.8.2", ">=2026.11.7.5"},
		{versionEightFourBootstrapTemplate, "2026.11.8.4", ">=2026.11.7.5"},
		{versionEightThreeBootstrapTemplate, "2026.11.8.3", ">=2026.11.7.5"},
		{versionEightOneBootstrapTemplate, "2026.11.8.1", ">=2026.11.7.5"},
		{versionSevenFiveBootstrapTemplate, "2026.11.7.5", ">=2026.11.7.5"},
		{versionSevenThreeBootstrapTemplate, "2026.11.7.3", ">=2026.11.7.3"},
		{previousVersionedBootstrapTemplate, "2026.11.6.10", ">=2026.11.6.9"},
		{versionElevenBootstrapTemplate, "2026.11.6.11", ">=2026.11.6.9"},
		{versionThirteenBootstrapTemplate, "2026.11.6.13", ">=2026.11.6.9"},
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
	available := BootstrapVersionForScope(scope)
	result := BootstrapUpdate{Path: path, Scope: scope, Installed: b.Revision, Available: available, CLIRange: BootstrapCLIRange}
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
	current, err := BootstrapTextForScope(b.Policy, scope)
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
	if b.Revision != "" && VersionLess(available, b.Revision) {
		result.State = "newer"
		return result, nil
	}
	result.State = "modified"
	result.Action = "Local skill content differs. Preserve local edits and report the difference; modified content alone is not an upgrade offer."
	if RecognizedBootstrap(data, b.Policy) && (b.Revision == "" || VersionLess(b.Revision, available)) {
		if runtimeversion.Release.MatchString(version) {
			supported, err := BootstrapSupports(BootstrapCLIRange, version)
			if err != nil {
				return result, err
			}
			if !supported {
				result.State = "incompatible"
				result.Compatible = &supported
				result.Action = "Use a CLI matching the offered skill's doomer-cli-range before upgrading the skill. Keep the installed skill until then."
				return result, nil
			}
		}
		result.State = "update_available"
		result.UpgradeAvailable = true
		result.Action = BootstrapUpgradeOffer(scope) + " After acceptance, run doomer bootstrap upgrade with the same scope. Preserve the update policy."
	}
	return result, nil
}

// VerifyPublishedBootstrap checks the canonical stable skill distributed publicly.
// The checked file cannot choose the policy used to generate its expected bytes.
func VerifyPublishedBootstrap(data []byte) error {
	expected, err := BootstrapText(Policy{Protocol: Protocol, Channel: "stable"})
	if err != nil {
		return err
	}
	if !bytes.Equal(data, []byte(expected)) {
		return fmt.Errorf("public skill does not match CLI bootstrap %s (CLI range %s)", BootstrapVersion, BootstrapCLIRange)
	}
	return nil
}
