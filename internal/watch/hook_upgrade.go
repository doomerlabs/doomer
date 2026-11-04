package watch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/doomerlabs/doomer/internal/selfupdate"
)

// Read only the install reference's version heading, never executable commands.
var skillReleaseHeading = regexp.MustCompile(`(?m)^## Select CLI release (20[0-9]{2}\.[1-9][0-9]?\.[1-9][0-9]?\.[1-9][0-9]*)\r?$`)

func hookUpgradeNotice(root, repo, session string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return localUpgradeNotice(root, repo, session, selfupdate.Updater{Home: home})
}

func localUpgradeNotice(root, repo, session string, updater selfupdate.Updater) string {
	reference, version := repositorySkillPin(repo)
	if version == "" {
		return ""
	}
	// Once per chat and pin, including concurrent hooks and later turns. A declined
	// upgrade stays declined in this chat; a changed pin can produce a new offer.
	key := Key(session + "\x00" + reference + "\x00" + version)
	receipt := filepath.Join(root, "upgrade-notices", key+".json")
	message := ""
	_ = Locked(root, "upgrade-"+key, func() error {
		var delivered bool
		if Read(receipt, &delivered) == nil && delivered {
			return nil
		}
		stamp, stampErr := updater.InstallationStamp()
		checkFile := filepath.Join(root, "upgrade-checks", key+".json")
		var checked upgradeCheck
		if stampErr == nil && Read(checkFile, &checked) == nil && checked.Stamp == stamp && time.Since(checked.At) >= 0 && time.Since(checked.At) < 5*time.Minute {
			return nil
		}
		status, err := updater.Check(version)
		if err != nil {
			return nil
		}
		if status.State != "update_available" || !status.Managed {
			if stampErr == nil {
				_ = Write(checkFile, upgradeCheck{Stamp: stamp, At: time.Now()})
			}
			return nil
		}
		if err := Write(receipt, true); err != nil {
			return err
		}
		message = fmt.Sprintf("Doomer's managed CLI is %s; this repository's installed skill requires %s. Reread the local doomer-review skill and its install reference at %q. Briefly offer the user the update with a backup, unless they already declined this version or skipped Doomer for this chat. Require the user's approval before running the verified self-update workflow. Existing setup and upload consent do not authorize an upgrade. Do not repeat this notice or reinstall hooks merely because an update is available.", status.Version, version, reference)
		return nil
	})
	return message
}

type upgradeCheck struct {
	Stamp string
	At    time.Time
}

func repositorySkillPin(repo string) (string, string) {
	// npx skills uses .agents for Codex and may link Claude's copy to it.
	reference, version := "", ""
	for _, directory := range []string{".agents", ".claude"} {
		path := filepath.Join(repo, directory, "skills", "doomer-review", "references", "install.md")
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() > 64<<10 {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		st, err = f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() > 64<<10 {
			f.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
		f.Close()
		if err != nil || len(data) > 64<<10 {
			continue
		}
		matches := skillReleaseHeading.FindAllSubmatch(data, -1)
		if len(matches) != 1 {
			return "", ""
		}
		pin := string(matches[0][1])
		if version != "" && pin != version {
			return "", ""
		}
		if reference == "" {
			reference, version = path, pin
		}
	}
	return reference, version
}
