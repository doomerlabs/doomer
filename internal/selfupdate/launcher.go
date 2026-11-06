package selfupdate

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
)

// InstallLauncher is an explicit migration of the convenient global command.
// Releases remain immutable and existing global installations receive a backup.
func (u Updater) InstallLauncher(required string, adopt bool) (Status, error) {
	versioned := u
	versioned.Versioned = true
	source, err := versioned.Check(required)
	if err != nil || source.State != "ready" {
		return source, errors.Join(err, errors.New("install and verify the versioned release first"))
	}
	if err := runtimeversion.Supports(context.Background(), source.Path); err != nil {
		return source, err
	}
	legacy := u
	legacy.Versioned = false
	before, err := legacy.Check(required)
	if err != nil {
		return before, err
	}
	if before.State == "ready" || before.State == "newer_installed" {
		return before, nil
	}
	if !before.Managed && before.State != "missing" && !adopt {
		return before, fmt.Errorf("global command is unmanaged; obtain approval and use --adopt")
	}
	r, err := u.root(true)
	if err != nil {
		return before, err
	}
	defer r.Close()
	lock, err := publock.TryAcquire(filepath.Join(u.Home, ".local/share/doomer"), "installation")
	if err != nil {
		return before, err
	}
	defer lock.Close()
	current, err := legacy.status(r, required)
	if err == nil && current.Managed && (current.State == "ready" || current.State == "newer_installed") {
		return current, nil
	}
	if err != nil || current.State != before.State || current.SHA256 != before.SHA256 {
		return current, errors.Join(err, errors.New("global command changed during launcher installation"))
	}
	sourcePath, _ := versioned.installPaths(required)
	data, err := readRegular(r, sourcePath, maxBinary)
	if err != nil || hash(data) != source.SHA256 {
		return source, errors.Join(err, errors.New("versioned release changed during launcher installation"))
	}
	next, recordNext := binaryPath+".next-"+rand.Text(), recordPath+".next-"+rand.Text()
	defer r.Remove(next)
	defer r.Remove(recordNext)
	if err := writeNew(r, next, data, 0755); err != nil {
		return before, err
	}
	rec, _ := json.Marshal(Record{1, "doomer-review", before.Path, required, source.SHA256})
	if err := writeNew(r, recordNext, rec, 0600); err != nil {
		return before, err
	}
	backup, err := publish(r, next, recordNext, before.State != "missing")
	if err != nil {
		return before, err
	}
	result, err := legacy.status(r, required)
	if backup != "" {
		result.Backup = filepath.Join(u.Home, backup)
	}
	return result, err
}

// NewestInstalled selects only checksum-verified immutable releases. Directory
// names and partial downloads cannot advance the global command by themselves.
func (u Updater) NewestInstalled(required string) (string, error) {
	if _, err := compare(required, required); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(filepath.Join(u.Home, ".local/share/doomer/versions"))
	if os.IsNotExist(err) {
		return required, nil
	}
	if err != nil {
		return "", err
	}
	u.Versioned = true
	newest := required
	for _, entry := range entries {
		comparison, err := compare(entry.Name(), newest)
		if err != nil || comparison <= 0 || !entry.IsDir() {
			continue
		}
		status, err := u.Check(entry.Name())
		if err != nil {
			return "", err
		}
		if status.State == "ready" {
			newest = entry.Name()
		}
	}
	return newest, nil
}
