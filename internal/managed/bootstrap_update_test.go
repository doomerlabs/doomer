package managed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapUpgradeOffer(t *testing.T) {
	project := BootstrapUpgradeOffer("project")
	for _, text := range []string{
		"A new version of the Doomer skill is available. Update this repository’s skill files now? Your current version still works.",
		"‘Update skill (recommended)’, ‘Maybe later’",
		"check which skill files exist in Git HEAD",
		"open a pull request containing only those skill updates",
		"leave it untracked",
	} {
		if !strings.Contains(project, text) {
			t.Fatalf("project offer missing %q: %s", text, project)
		}
	}
	personal := BootstrapUpgradeOffer("personal")
	if !strings.Contains(personal, "Update your personal skill files now?") || !strings.Contains(personal, "without creating a pull request") || strings.Contains(personal, "Git HEAD") {
		t.Fatalf("incorrect personal offer: %s", personal)
	}
}

func TestBootstrapVersionRanges(t *testing.T) {
	for _, tc := range []struct {
		constraint, version string
		want                bool
	}{
		{">=2026.11.6.9 <2027.1.1.1", "2026.11.6.10", true},
		{">=2026.11.6.9", "2026.11.6.8", false},
		{">2026.11.6.9 <=2026.11.6.11", "2026.11.6.9", false},
		{">2026.11.6.9 <=2026.11.6.11", "2026.11.6.11", true},
		{"2026.11.6.9", "2026.11.6.9", true},
		{"=2026.11.6.9", "2026.11.6.10", false},
		{">=2026.11.6.9", "dev", false},
	} {
		got, err := BootstrapSupports(tc.constraint, tc.version)
		if err != nil || got != tc.want {
			t.Fatalf("%s / %s: %v %v", tc.constraint, tc.version, got, err)
		}
	}
	for _, invalid := range []string{"", "*", ">=1.2.3", "^2026.11.6.9", ">=oops", ">=2026.11.6.9 junk"} {
		if _, err := BootstrapSupports(invalid, BootstrapVersion); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestBootstrapInspectionRecognizesUnmarkedAndCustomWrappers(t *testing.T) {
	p := Policy{Protocol: Protocol, Channel: "pinned", Version: "2026.11.6.9"}
	current, _ := BootstrapText(p)
	legacy, _ := bootstrapText(p, legacyBootstrapTemplate, "", "")
	previous, _ := bootstrapText(p, previousVersionedBootstrapTemplate, "2026.11.6.10", ">=2026.11.6.9")
	previousThirteen, _ := bootstrapText(p, versionThirteenBootstrapTemplate, "2026.11.6.13", ">=2026.11.6.9")
	previousTwo, _ := bootstrapText(p, versionEightTwoBootstrapTemplate, "2026.11.8.2", ">=2026.11.7.5")
	previousThree, _ := bootstrapText(p, versionEightThreeBootstrapTemplate, "2026.11.8.3", ">=2026.11.7.5")
	path := filepath.Join(t.TempDir(), "SKILL.md")
	for _, tc := range []struct{ text, state string }{
		{current, "current"}, {legacy, "update_available"}, {previous, "update_available"},
		{previousThirteen, "update_available"}, {previousTwo, "update_available"}, {previousThree, "update_available"},
		{legacy + "\nCustom instructions.\n", "modified"},
		{previous + "\nCustom instructions.\n", "modified"},
		{strings.Replace(current, BootstrapVersion, "2027.1.1.1", 1), "newer"},
	} {
		if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := InspectBootstrap(path, "project", BootstrapVersion)
		if err != nil || result.State != tc.state {
			t.Fatalf("%s: %+v %v", tc.state, result, err)
		}
		if got := RecognizedBootstrap([]byte(tc.text), p); got != (tc.state == "current" || tc.state == "update_available") {
			t.Fatalf("recognition for %s: %v", tc.state, got)
		}
		b, err := ReadBootstrap(path, "project")
		if err != nil || b.Policy != p {
			t.Fatal(b, err)
		}
	}
	if err := os.WriteFile(path, []byte(current), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := InspectBootstrap(path, "project", "2026.11.6.8")
	if err != nil || result.State != "incompatible" {
		t.Fatal(result, err)
	}
}

func TestSevenFiveBootstrapRemainsUpgradable(t *testing.T) {
	policy := Policy{Protocol: Protocol, Channel: "stable"}
	previous, err := bootstrapText(policy, versionSevenFiveBootstrapTemplate, "2026.11.7.5", ">=2026.11.7.5")
	if err != nil {
		t.Fatal(err)
	}
	if !RecognizedBootstrap([]byte(previous), policy) {
		t.Fatal("released wrapper was treated as a custom edit")
	}
	current, err := BootstrapText(policy)
	if err != nil {
		t.Fatal(err)
	}
	if previous == current || !strings.Contains(current, "without `--prompt`") {
		t.Fatal("updated onboarding guidance missing")
	}
}

func TestPromptRulesAreSharedByBootstrapAndRuntime(t *testing.T) {
	bootstrap, err := BootstrapText(Policy{Protocol: Protocol, Channel: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	for _, rules := range []string{PermissionPromptRules, UpgradePromptRules} {
		if !strings.Contains(bootstrap, strings.TrimSpace(rules)) || !strings.Contains(Instructions, strings.TrimSpace(rules)) {
			t.Fatal("prompt decision rules diverged")
		}
	}
	for _, text := range []string{bootstrap, Instructions} {
		if strings.Contains(text, "{{permission_prompt_rules}}") || strings.Contains(text, "{{upgrade_prompt_rules}}") {
			t.Fatal("unexpanded prompt rules")
		}
	}
}

func TestEightOneBootstrapRemainsUpgradable(t *testing.T) {
	policy := Policy{Protocol: Protocol, Channel: "stable"}
	old, err := bootstrapText(policy, versionEightOneBootstrapTemplate, "2026.11.8.1", ">=2026.11.7.5")
	if err != nil {
		t.Fatal(err)
	}
	if !RecognizedBootstrap([]byte(old), policy) {
		t.Fatal("prior wrapper treated as modified")
	}
	current, err := BootstrapText(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"doomer preflight", "continue authorized local edits and tests by default", "Explicit earlier user requirements to wait", "account or authorize uploads"} {
		if !strings.Contains(current, want) {
			t.Fatal("missing contract", want)
		}
	}
	_, continuation, ok := strings.Cut(current, "If review is blocked,")
	if !ok {
		t.Fatal("missing continuation policy")
	}
	continuation, _, ok = strings.Cut(continuation, "Show complete clickable")
	if !ok {
		t.Fatal("missing continuation end boundary")
	}
	for _, want := range []string{
		"Explicit earlier user requirements to wait or\nrequire review before editing take precedence.",
		"Do not ask whether to continue.",
		"Stop the denied transmission; keep uploads blocked.",
		"Silence is not upload consent.",
		"Do not retry explicit reviewer refusals.",
	} {
		if !strings.Contains(continuation, want) {
			t.Fatal("missing local continuation boundary", want)
		}
	}
	if strings.Contains(current, "ask once whether to continue local") {
		t.Fatal("local work still gated")
	}
}

func TestRepositoryBootstrapsMatchGenerated(t *testing.T) {
	current, err := BootstrapText(Policy{Protocol: Protocol, Channel: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{".agents", ".claude"} {
		data, err := os.ReadFile(filepath.Join("..", "..", agent, "skills", "doomer-review", "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != current {
			t.Fatal("repository bootstrap differs from generated template", agent)
		}
	}
}
