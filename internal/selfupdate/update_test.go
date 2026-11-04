package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const release = "2026.11.3.5"

func bundle(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "./doomer", Mode: 0755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
func fixture(t *testing.T) (Updater, *int) {
	t.Helper()
	archive := bundle(t, []byte("verified-new-binary"))
	requests := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		if strings.HasSuffix(r.URL.Path, "release-manifest.json") {
			json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "version": release, "artifacts": map[string]string{"doomer_" + release + "_linux_amd64.tar.gz": "sha256:" + hash(archive)}})
		} else {
			w.Write(archive)
		}
	}))
	t.Cleanup(srv.Close)
	return Updater{Home: t.TempDir(), BaseURL: srv.URL, OS: "linux", Arch: "amd64"}, requests
}
func put(t *testing.T, u Updater, path string, data []byte) {
	t.Helper()
	f := filepath.Join(u.Home, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(f), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, data, 0755); err != nil {
		t.Fatal(err)
	}
}
func managed(t *testing.T, u Updater, version string) {
	t.Helper()
	body := []byte("old-binary")
	put(t, u, binaryPath, body)
	rec, _ := json.Marshal(Record{1, "doomer-review", filepath.Join(u.Home, filepath.FromSlash(binaryPath)), version, hash(body)})
	put(t, u, recordPath, rec)
}
func TestInstallUpgradeAndReadyNoop(t *testing.T) {
	u, requests := fixture(t)
	first, err := u.Apply(context.Background(), release, false)
	if err != nil || !first.Managed || first.State != "ready" {
		t.Fatalf("%+v %v", first, err)
	}
	if *requests != 2 {
		t.Fatal(*requests)
	}
	if _, err := u.Apply(context.Background(), release, false); err != nil {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatal("ready check downloaded again")
	}
	managed(t, u, "2026.11.3.4")
	result, err := u.Apply(context.Background(), release, false)
	if err != nil || result.Backup == "" {
		t.Fatalf("%+v %v", result, err)
	}
	old, err := os.ReadFile(result.Backup)
	if err != nil || string(old) != "old-binary" {
		t.Fatal("missing backup", err)
	}
	stat, _ := os.Stat(filepath.Join(u.Home, filepath.FromSlash(recordPath)))
	if stat.Mode().Perm() != 0600 {
		t.Fatal("record permissions", stat.Mode())
	}
}
func TestUnmanagedOrModifiedRequiresAdoption(t *testing.T) {
	for _, modified := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmanaged", true: "modified"}[modified], func(t *testing.T) {
			u, requests := fixture(t)
			if modified {
				managed(t, u, "2026.11.3.4")
			}
			put(t, u, binaryPath, []byte("manual replacement"))
			s, err := u.Check(release)
			if err != nil || s.Managed {
				t.Fatal(s, err)
			}
			if _, err := u.Apply(context.Background(), release, false); err == nil {
				t.Fatal("silently adopted")
			}
			if *requests != 0 {
				t.Fatal("unapproved download")
			}
			result, err := u.Apply(context.Background(), release, true)
			if err != nil || !result.Managed {
				t.Fatal(result, err)
			}
			backup, _ := os.ReadFile(result.Backup)
			if string(backup) != "manual replacement" {
				t.Fatal("lost original")
			}
		})
	}
}
func TestNewerManagedVersionNeverDowngrades(t *testing.T) {
	u, requests := fixture(t)
	managed(t, u, "2026.11.4.1")
	s, err := u.Check(release)
	if err != nil || s.State != "newer_installed" {
		t.Fatal(s, err)
	}
	if _, err := u.Apply(context.Background(), release, true); err == nil {
		t.Fatal("downgrade accepted")
	}
	if *requests != 0 {
		t.Fatal("downloaded downgrade")
	}
}
func TestSymlinkAdoptionPreservesTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary")
	}
	u, requests := fixture(t)
	target := filepath.Join(t.TempDir(), "brew-doomer")
	os.WriteFile(target, []byte("brew"), 0755)
	os.MkdirAll(filepath.Join(u.Home, ".local/bin"), 0700)
	os.Symlink(target, filepath.Join(u.Home, filepath.FromSlash(binaryPath)))
	if _, err := u.Apply(context.Background(), release, false); err == nil {
		t.Fatal("followed unmanaged link")
	}
	if *requests != 0 {
		t.Fatal(*requests)
	}
	result, err := u.Apply(context.Background(), release, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "brew" {
		t.Fatal("modified package-manager binary")
	}
	if got, err := os.Readlink(result.Backup); err != nil || got != target {
		t.Fatal("link not backed up", got, err)
	}
}
func TestSymlinkParentRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary")
	}
	u, _ := fixture(t)
	os.Symlink(t.TempDir(), filepath.Join(u.Home, ".local"))
	if _, err := u.Check(release); err == nil {
		t.Fatal("symlink parent accepted")
	}
}
func TestInvalidDownloadsLeaveInstalledBinaryUntouched(t *testing.T) {
	for _, bad := range []string{"checksum", "manifest", "symlink executable", "duplicate executable"} {
		t.Run(bad, func(t *testing.T) {
			archive := bundle(t, []byte("new"))
			if bad == "symlink executable" || bad == "duplicate executable" {
				var b bytes.Buffer
				gz := gzip.NewWriter(&b)
				tw := tar.NewWriter(gz)
				if bad == "symlink executable" {
					tw.WriteHeader(&tar.Header{Name: "./doomer", Typeflag: tar.TypeSymlink, Linkname: "/outside", Mode: 0755})
				} else {
					for range 2 {
						tw.WriteHeader(&tar.Header{Name: "./doomer", Mode: 0755, Size: 1})
						tw.Write([]byte("x"))
					}
				}
				tw.Close()
				gz.Close()
				archive = b.Bytes()
			}
			sum := hash(archive)
			if bad == "checksum" {
				sum = strings.Repeat("0", 64)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "release-manifest.json") {
					v := release
					if bad == "manifest" {
						v = "2026.1.1.1"
					}
					json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "version": v, "artifacts": map[string]string{"doomer_" + release + "_linux_amd64.tar.gz": "sha256:" + sum}})
				} else {
					w.Write(archive)
				}
			}))
			defer srv.Close()
			u := Updater{Home: t.TempDir(), BaseURL: srv.URL, OS: "linux", Arch: "amd64"}
			managed(t, u, "2026.11.3.4")
			if _, err := u.Apply(context.Background(), release, false); err == nil {
				t.Fatal("invalid release accepted")
			}
			raw, _ := os.ReadFile(filepath.Join(u.Home, filepath.FromSlash(binaryPath)))
			if string(raw) != "old-binary" {
				t.Fatal("overwrote existing binary")
			}
		})
	}
}
func TestPublishRestoresBinaryWhenRecordCannotBeSaved(t *testing.T) {
	u, _ := fixture(t)
	put(t, u, binaryPath, []byte("original"))
	put(t, u, binaryPath+".next", []byte("next"))
	put(t, u, recordPath+".next", []byte("record"))
	os.Mkdir(filepath.Join(u.Home, filepath.FromSlash(recordPath)), 0700)
	r, err := u.root(false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := publish(r, binaryPath+".next", recordPath+".next", true); err == nil {
		t.Fatal("expected failed record publication")
	}
	raw, _ := r.ReadFile(binaryPath)
	if string(raw) != "original" {
		t.Fatal("did not restore original")
	}
}
func TestCheckDoesNotCreateDirectories(t *testing.T) {
	u, _ := fixture(t)
	s, err := u.Check(release)
	if err != nil || s.State != "missing" {
		t.Fatal(s, err)
	}
	entries, _ := os.ReadDir(u.Home)
	if len(entries) != 0 {
		t.Fatal("read-only check wrote files")
	}
}

func TestSideBySideInstallPreservesGlobalAndOtherReleases(t *testing.T) {
	u, requests := fixture(t)
	managed(t, u, "2026.11.3.4")
	globalBefore, _ := os.ReadFile(filepath.Join(u.Home, binaryPath))
	u.Versioned = true
	result, err := u.Apply(context.Background(), release, false)
	if err != nil || result.State != "ready" {
		t.Fatal(result, err)
	}
	if result.Path == filepath.Join(u.Home, binaryPath) {
		t.Fatal("versioned install replaced launcher")
	}
	globalAfter, _ := os.ReadFile(filepath.Join(u.Home, binaryPath))
	if !bytes.Equal(globalBefore, globalAfter) {
		t.Fatal("changed global CLI")
	}
	other := release + "1"
	otherBinary, otherRecord := u.installPaths(other)
	put(t, u, otherBinary, []byte("other release"))
	rec, _ := json.Marshal(Record{1, "doomer-review", filepath.Join(u.Home, otherBinary), other, hash([]byte("other release"))})
	put(t, u, otherRecord, rec)
	if status, err := u.Check(other); err != nil || status.State != "ready" {
		t.Fatal(status, err)
	}
	if _, err := u.Apply(context.Background(), release, false); err != nil {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatal("re-downloaded immutable release")
	}
	put(t, u, otherBinary, []byte("modified"))
	if _, err := u.Apply(context.Background(), other, true); err == nil {
		t.Fatal("replaced an immutable release")
	}
}

func TestLauncherMigrationPreservesOldBinary(t *testing.T) {
	u, _ := fixture(t)
	managed(t, u, "2026.11.3.4")
	before, _ := os.ReadFile(filepath.Join(u.Home, binaryPath))
	versioned := u
	versioned.Versioned = true
	binary, record := versioned.installPaths(release)
	body := []byte("#!/bin/sh\nprintf '{\"protocol\":1}\\n'\n")
	put(t, u, binary, body)
	rec, _ := json.Marshal(Record{1, "doomer-review", filepath.Join(u.Home, binary), release, hash(body)})
	put(t, u, record, rec)
	result, err := versioned.InstallLauncher(release, false)
	if err != nil || result.State != "ready" || result.Backup == "" {
		t.Fatal(result, err)
	}
	backup, err := os.ReadFile(result.Backup)
	if err != nil || !bytes.Equal(before, backup) {
		t.Fatal("lost old CLI", err)
	}
	if got, err := versioned.Check(release); err != nil || got.State != "ready" {
		t.Fatal("changed versioned release", got, err)
	}
}
