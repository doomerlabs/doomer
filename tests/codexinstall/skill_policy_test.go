package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
)

func TestGlobalRuntimeInstallHonorsSkillPolicy(t *testing.T) {
	for _, policy := range []managed.Policy{
		{Protocol: managed.Protocol, Channel: "stable"},
		{Protocol: managed.Protocol, Channel: "pinned", Version: managed.BootstrapVersion},
		{Protocol: managed.Protocol, Channel: "delayed", DelayHours: 24},
	} {
		t.Run(policy.Channel, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".agents/skills/doomer-review/SKILL.md")
			text, err := managed.BootstrapText(policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			args, err := globalRuntimeInstallArgs(home)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"install", "--runtime", "--personal", "--policy", policy.Channel}
			if policy.Version != "" {
				want = append(want, "--version", policy.Version)
			}
			if policy.DelayHours != 0 {
				want = append(want, "--delay-hours", "24")
			}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("%v != %v", args, want)
			}
			if after, _ := os.ReadFile(path); string(after) != text {
				t.Fatal("changed the npx-owned skill")
			}
		})
	}
}

func TestUpgradeFixturePreservesHistoricalStablePolicy(t *testing.T) {
	pinned, err := managed.BootstrapText(managed.Policy{Protocol: managed.Protocol, Channel: "pinned", Version: managed.BootstrapVersion})
	if err != nil {
		t.Fatal(err)
	}
	stable, err := managed.BootstrapText(managed.Policy{Protocol: managed.Protocol, Channel: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	original := config{Scenario: "upgrade-personal-npx", SkillsBootstrap: pinned, RepoBootstraps: map[string]string{".agents": pinned}}
	fixture := stableUpgradeFixture(original)
	if fixture.SkillsBootstrap != stable || fixture.RepoBootstraps[".agents"] != stable {
		t.Fatal("historical upgrade fixture changed more than policy")
	}
	if original.RepoBootstraps[".agents"] != pinned {
		t.Fatal("modified a shared fixture map")
	}
	original.Scenario = "global-npx-discovery"
	if stableUpgradeFixture(original).SkillsBootstrap != pinned {
		t.Fatal("changed the public pinned skill scenario")
	}
}
