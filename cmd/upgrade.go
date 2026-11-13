package cmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/rootreplace"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	"github.com/doomerlabs/doomer/internal/version"
	"github.com/spf13/cobra"
)

type cliInstallation struct {
	Manager     string   `json:"manager"`
	Executable  string   `json:"executable"`
	ManagerHome string   `json:"manager_home,omitempty"`
	Command     []string `json:"upgrade_command,omitempty"`
}

var upgradeExecutable = os.Executable
var upgradeRun = func(ctx context.Context, c *cobra.Command, command []string) error {
	child := exec.CommandContext(ctx, command[0], command[1:]...)
	child.Stdin, child.Stdout, child.Stderr = c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr()
	return child.Run()
}
var upgradeCatalog = func(ctx context.Context) (managed.Catalog, error) {
	return managed.FetchCatalog(ctx, &http.Client{Timeout: 15 * time.Second})
}

func inspectCLIInstallation() (cliInstallation, error) {
	path, err := upgradeExecutable()
	if err != nil {
		return cliInstallation{}, err
	}
	return identifyCLIInstallation(path)
}

func identifyCLIInstallation(path string) (cliInstallation, error) {
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return cliInstallation{}, err
	}
	actual, err = filepath.Abs(actual)
	if err != nil {
		return cliInstallation{}, err
	}
	result := cliInstallation{Manager: "direct", Executable: actual}
	// Homebrew's versioned Cellar path is authoritative; never replace its file.
	cellar := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(actual))))
	if filepath.Base(actual) == "doomer" && filepath.Base(filepath.Dir(actual)) == "bin" && filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(actual)))) == "doomer" && filepath.Base(cellar) == "Cellar" {
		result.Manager = "homebrew"
		result.Command = []string{"brew", "upgrade", "doomer"}
		return result, nil
	}
	home, err := defaultManagerHome()
	if err != nil {
		return result, err
	}
	homes := []string{home}
	if explicit := os.Getenv("DOOMER_DEV_HOME"); explicit != "" {
		homes = append(homes, explicit)
	} else {
		homes = append(homes, home+"-dev")
	}
	for _, home := range homes {
		var state struct {
			Native struct {
				Path string `json:"path"`
				SHA  string `json:"sha256"`
			} `json:"native"`
			Runtime string `json:"runtime"`
		}
		raw, readErr := os.ReadFile(filepath.Join(home, "state.json"))
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return result, readErr
		}
		if len(raw) > 1<<20 || json.Unmarshal(raw, &state) != nil {
			return result, fmt.Errorf("invalid Node manager state")
		}
		native, nativeErr := filepath.EvalSymlinks(state.Native.Path)
		if nativeErr != nil || native != actual {
			continue
		}
		data, readErr := os.ReadFile(actual)
		if readErr != nil {
			return result, readErr
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != state.Native.SHA {
			return result, fmt.Errorf("Node-managed CLI differs from its recorded checksum")
		}
		if !filepath.IsAbs(state.Runtime) {
			return result, fmt.Errorf("Node manager executable is unavailable; run npx @doomerlabs/doomer update")
		}
		result.Manager = "node"
		result.ManagerHome = home
		result.Command = []string{"node", state.Runtime, "upgrade", "--home", home}
		return result, nil
	}
	// Immutable old managed runtimes and development builds are never adopted.
	if strings.Contains(actual, filepath.FromSlash("/.local/share/doomer/versions/")) {
		result.Manager = "legacy"
	}
	return result, nil
}

func newUpgradeCommand() *cobra.Command {
	var check, asJSON bool
	c := &cobra.Command{Use: "upgrade", Short: "Update only the CLI through its installation manager", Args: cobra.NoArgs, Long: "Update the CLI without installing skills or changing hooks. Homebrew installations use brew upgrade doomer; Node-managed installations use the installed manager's CLI-only upgrade. Direct stable binaries use the signed release catalog. Development and legacy pinned runtimes are not replaced."}
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.Flags().BoolVar(&check, "check", false, "check for updates without installing")
	c.Flags().BoolVar(&asJSON, "json", false, "print structured update status")
	c.RunE = func(c *cobra.Command, _ []string) error {
		installation, err := inspectCLIInstallation()
		if err != nil {
			return err
		}
		if installation.Manager == "legacy" {
			return fmt.Errorf("this runtime is pinned by the legacy skill; update that installation instead")
		}
		if installation.Manager == "node" {
			command := append([]string{}, installation.Command...)
			if check {
				command = append(command, "--check")
			}
			if asJSON {
				command = append(command, "--json")
			}
			return upgradeRun(c.Context(), c, command)
		}
		if installation.Manager == "homebrew" {
			command := installation.Command
			if check {
				command = []string{"brew", "outdated", "doomer"}
				if asJSON {
					command = append(command, "--json=v2")
				}
			}
			return upgradeRun(c.Context(), c, command)
		}
		if !runtimeversion.Release.MatchString(version.Version) {
			return fmt.Errorf("this is a development build; rebuild it yourself or install a released CLI")
		}
		catalog, err := upgradeCatalog(c.Context())
		if err != nil {
			return err
		}
		var selected managed.Release
		for _, r := range catalog.Releases {
			if r.Protocol == managed.Protocol && !r.Withdrawn && (selected.Version == "" || managed.VersionLess(selected.Version, r.Version)) {
				selected = r
			}
		}
		if selected.Version == "" {
			return fmt.Errorf("no supported stable CLI release is available")
		}
		available := managed.VersionLess(version.Version, selected.Version)
		if !check && available {
			before, err := os.Lstat(installation.Executable)
			if err != nil {
				return err
			}
			updater := selfupdate.Updater{ExpectedManifestSHA256: selected.ManifestSHA256}
			binary, err := updater.DownloadVerified(c.Context(), selected.Version)
			if err != nil {
				return err
			}
			if err = replaceCLI(installation.Executable, binary, before); err != nil {
				return err
			}
		}
		state := "current"
		if available {
			state = "update_available"
			if !check {
				state = "updated"
			}
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"state": state, "current_version": version.Version, "latest_version": selected.Version, "manager": installation.Manager})
		}
		_, err = fmt.Fprintf(c.OutOrStdout(), "Doomer %s: %s → %s\n", state, version.Version, selected.Version)
		return err
	}
	return c
}

// The file must still be the one inspected before download. No CLI is run here.
func replaceCLI(path string, data []byte, before os.FileInfo) error {
	if !before.Mode().IsRegular() {
		return fmt.Errorf("CLI destination is not a regular file")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	name := ".doomer-upgrade-" + rand.Text()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	if err = file.Chmod(before.Mode().Perm()); err == nil {
		_, err = file.Write(data)
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	current, err := root.Lstat(filepath.Base(path))
	if err != nil {
		return err
	}
	if !os.SameFile(before, current) {
		return fmt.Errorf("CLI changed during upgrade")
	}
	return rootreplace.Mutable(root, name, filepath.Base(path))
}

func newStatusCommand() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{Use: "status", Short: "Inspect the local CLI installation without changing it", Args: cobra.NoArgs}
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.Flags().BoolVar(&asJSON, "json", false, "print structured installation status")
	c.RunE = func(c *cobra.Command, _ []string) error {
		installation, err := inspectCLIInstallation()
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"version": version.Version, "installation": installation})
		}
		_, err = fmt.Fprintf(c.OutOrStdout(), "Doomer %s\nInstallation: %s\nExecutable: %s\n", version.Version, installation.Manager, installation.Executable)
		return err
	}
	return c
}
