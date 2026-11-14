package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestEveryCommandHasMarkdownHelp(t *testing.T) {
	root := NewRootCommand()
	var check func(*cobra.Command)
	check = func(command *cobra.Command) {
		t.Run(command.CommandPath(), func(t *testing.T) {
			var output bytes.Buffer
			command.SetOut(&output)
			if err := command.Help(); err != nil {
				t.Fatal(err)
			}
			description := command.Long
			if description == "" {
				description = command.Short
			}
			for _, expected := range []string{"# " + command.CommandPath(), "## Usage", "```sh", "--help", strings.TrimSpace(description)} {
				if !strings.Contains(output.String(), expected) {
					t.Errorf("missing %q in help:\n%s", expected, output.String())
				}
			}
			if command.Short == "" {
				t.Error("command needs a description")
			}
			command.Flags().VisitAll(func(flag *pflag.Flag) {
				if flag.Hidden {
					return
				}
				if !strings.Contains(output.String(), "--"+flag.Name) {
					t.Errorf("help omitted flag --%s", flag.Name)
				}
			})
		})
		for _, child := range command.Commands() {
			check(child)
		}
	}
	check(root)
	var output bytes.Buffer
	root.SetOut(&output)
	if err := root.Help(); err != nil {
		t.Fatal(err)
	}
	for _, child := range root.Commands() {
		present := strings.Contains(output.String(), "`"+child.CommandPath()+"`")
		if present == child.Hidden {
			t.Errorf("unexpected visibility for %s", child.CommandPath())
		}
		for _, nested := range child.Commands() {
			if strings.Contains(output.String(), "`"+nested.CommandPath()+"`") {
				t.Errorf("root help expands %s", nested.CommandPath())
			}
		}
	}

}

func TestManagedHelpThroughLauncher(t *testing.T) {
	// Invalid configuration and nonexistent task handles must not block help.
	t.Setenv("DOOMER_CONFIG_DIR", t.TempDir())
	t.Setenv("DOOMER_API_URL", "invalid")
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := launcherExecutable
	launcherExecutable = func() (string, error) { return filepath.Join(home, ".local", "bin", "doomer"), nil }
	t.Cleanup(func() { launcherExecutable = original })
	repo := newWatchTestRepo(t)
	putPin(t, repo, "2026.11.3.7")
	t.Chdir(repo)
	for _, args := range [][]string{
		{"task", "respond", "--help"},
		{"task", "respond", "missing-handle", "--help"},
		{"task", "respond", "missing-handle", "-h"},
		{"prepare", "--task", "missing-handle", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			handled, err := DispatchCLI(context.Background(), args, strings.NewReader(""), &output, &output)
			if err != nil {
				t.Fatal(err)
			}
			if !handled {
				root := NewRootCommand()
				root.SetArgs(args)
				root.SetOut(&output)
				root.SetErr(&output)
				err = root.Execute()
			}
			if err != nil || !strings.Contains(output.String(), "## Usage") {
				t.Fatalf("help failed: %v\n%s", err, output.String())
			}
			if args[0] == "task" {
				for _, expected := range []string{"lead_id", "status", "agrees", "reason", "2000 bytes", "64-character", "sent=true", "<<'JSON'"} {
					if !strings.Contains(output.String(), expected) {
						t.Errorf("response help missing %q", expected)
					}
				}
			}
		})
	}
}

func TestHelpDescriptionsDoNotRelabelPartialMatches(t *testing.T) {
	root := NewRootCommand()
	watch, _, _ := root.Find([]string{"watch"})
	findings, _, _ := root.Find([]string{"watch", "findings"})
	watch.RemoveCommand(findings)
	before := watch.Short
	configureCommandHelp(root)
	if watch.Short != before {
		t.Fatalf("partial lookup replaced watch help: %q", watch.Short)
	}
}
