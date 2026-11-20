package cmd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	mdmTestVersion    = "2026.11.20.1-beta.2"
	mdmTestOldVersion = "2026.11.20.1-beta.1"
	mdmTestSigner     = "Developer ID Installer: Doomer Test (123456789A)"
	mdmTestPackage    = "signed package bytes"
)

type mdmUpdateFixture struct {
	updater                              mdmUpdater
	active, stage                        string
	channel, signer, manifest, feed, pkg string
	install                              func() error
	requests, installs                   int
}

func newMDMUpdateFixture(t *testing.T, current string) *mdmUpdateFixture {
	t.Helper()
	root := t.TempDir()
	f := &mdmUpdateFixture{
		active: filepath.Join(root, "doomer"), stage: filepath.Join(root, "incoming", "doomer"),
		channel: "beta", signer: mdmTestSigner, pkg: mdmTestPackage,
	}
	if err := os.Mkdir(filepath.Dir(f.stage), 0755); err != nil {
		t.Fatal(err)
	}
	mdmTestWrite(t, filepath.Join(root, "installer-identity"), mdmTestSigner)
	mdmTestWrite(t, filepath.Join(root, "bootstrap-doomer"), mdmTestOldVersion)
	if current != "" {
		mdmTestWrite(t, f.active, current)
	}
	f.manifest = fmt.Sprintf(`{"schemaVersion":2,"packageSHA256":"%x"}`, sha256.Sum256([]byte(mdmTestPackage)))
	f.feed = fmt.Sprintf(`{"schema_version":1,"version":"2026.11.21.1-beta.1","managed_package":{"version":"%s","manifest_sha256":"%x"}}`, mdmTestVersion, sha256.Sum256([]byte(f.manifest)))
	f.install = func() error { return os.WriteFile(f.stage, []byte(mdmTestVersion), 0755) }
	client := &http.Client{Transport: managedTransport(func(r *http.Request) (*http.Response, error) {
		f.requests++
		status, body := http.StatusOK, ""
		switch strings.TrimPrefix(r.URL.Path, "/cli") {
		case "/beta.json", "/stable.json":
			body = f.feed
		case "/releases/" + mdmTestVersion + "/mdm-release.json":
			body = f.manifest
		case "/releases/" + mdmTestVersion + "/Doomer-MDM-" + mdmTestVersion + ".pkg":
			body = f.pkg
		default:
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	f.updater = mdmUpdater{
		dir: root, origin: "https://downloads.example/cli", client: client,
		readChannel: func(context.Context) (string, error) { return f.channel, nil },
		checkPath:   func(path string, _ bool) error { _, err := os.Lstat(path); return err },
		run: func(_ context.Context, path string, args ...string) ([]byte, error) {
			switch path {
			case mdmPkgutil:
				return []byte("Status: signed by a certificate trusted by macOS\n  1. " + f.signer + "\n"), nil
			case mdmInstaller:
				f.installs++
				return nil, f.install()
			case f.active, f.stage:
				version, err := os.ReadFile(path)
				return []byte(fmt.Sprintf(`{"version":%q}`, version)), err
			default:
				return nil, fmt.Errorf("unexpected command: %s %v", path, args)
			}
		},
	}
	return f
}

func mdmTestWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0755); err != nil {
		t.Fatal(err)
	}
}

func (f *mdmUpdateFixture) assertActive(t *testing.T, want string) {
	t.Helper()
	got, err := os.ReadFile(f.active)
	if want == "" && errors.Is(err, os.ErrNotExist) {
		return
	}
	if string(got) != want || err != nil {
		t.Fatalf("active runtime = %q, %v, want %q", got, err, want)
	}
}

func TestMDMUpdateActivation(t *testing.T) {
	for _, tc := range []struct {
		name, current string
		wantInstalls  int
	}{
		{"fresh bootstrap", "", 1},
		{"available update", mdmTestOldVersion, 1},
		{"already current", mdmTestVersion, 0},
		{"would downgrade", "2026.11.20.1-beta.3", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMDMUpdateFixture(t, tc.current)
			status, err := f.updater.update(t.Context(), "")
			if err != nil || f.installs != tc.wantInstalls {
				t.Fatalf("update = %q, %v, installs = %d", status, err, f.installs)
			}
			if tc.wantInstalls == 0 {
				f.assertActive(t, tc.current)
				if f.requests != 1 {
					t.Fatalf("up-to-date check made %d requests", f.requests)
				}
			} else {
				f.assertActive(t, mdmTestVersion)
			}
		})
	}
}

func TestMDMUpdateHandoffDetectsBootstrapAndActiveRuntimeReplacement(t *testing.T) {
	for _, current := range []string{"", mdmTestOldVersion} {
		f := newMDMUpdateFixture(t, current)
		path := f.active
		if current == "" {
			path = filepath.Join(f.updater.dir, "bootstrap-doomer")
		}
		running, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mdmRuntimeReplaced(f.active, running) {
			t.Fatal("requested handoff before activation")
		}
		if _, err := f.updater.update(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
		if !mdmRuntimeReplaced(f.active, running) {
			t.Fatal("did not request handoff after activating a newer binary")
		}
	}
}

func TestMDMUpdateManualMode(t *testing.T) {
	f := newMDMUpdateFixture(t, mdmTestOldVersion)
	f.channel = "manual"
	if status, err := f.updater.update(t.Context(), ""); err != nil || !strings.Contains(status, "manual mode") || f.requests != 0 {
		t.Fatalf("manual mode = %q, %v, requests = %d", status, err, f.requests)
	}
	f.assertActive(t, mdmTestOldVersion)
	if _, err := f.updater.update(t.Context(), "beta"); err != nil {
		t.Fatal(err)
	}
	f.assertActive(t, mdmTestVersion)
}

func TestMDMUpdateKeepsRuntimeWhenNoManagedPackage(t *testing.T) {
	for _, channel := range []string{"beta", "stable"} {
		t.Run(channel, func(t *testing.T) {
			f := newMDMUpdateFixture(t, mdmTestOldVersion)
			f.channel = channel
			f.feed = `{"schema_version":1,"version":"2026.11.21.1-beta.1"}`
			if status, err := f.updater.update(t.Context(), ""); err != nil || !strings.Contains(status, channel+" channel has no managed") || f.installs != 0 {
				t.Fatalf("missing package = %q, %v, installs = %d", status, err, f.installs)
			}
			f.assertActive(t, mdmTestOldVersion)
		})
	}
}

func TestMDMUpdateRejectsUnverifiedPackage(t *testing.T) {
	for _, tc := range []struct {
		name, problem string
		change        func(*mdmUpdateFixture)
	}{
		{"tampered manifest", "checksum manifest", func(f *mdmUpdateFixture) { f.manifest += "\n" }},
		{"tampered package", "SHA-256 mismatch", func(f *mdmUpdateFixture) { f.pkg += "tampered" }},
		{"different trusted signer", "expected trusted Doomer Installer signature", func(f *mdmUpdateFixture) {
			f.signer = "Developer ID Installer: Somebody Else (123456789B)"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMDMUpdateFixture(t, mdmTestOldVersion)
			tc.change(f)
			if _, err := f.updater.update(t.Context(), ""); err == nil || !strings.Contains(err.Error(), tc.problem) || f.installs != 0 {
				t.Fatalf("update error = %v, installer calls = %d", err, f.installs)
			}
			f.assertActive(t, mdmTestOldVersion)
		})
	}
	if mdmTrustedSigner([]byte("Status: untrusted\n  1. "+mdmTestSigner), mdmTestSigner) {
		t.Fatal("accepted the expected identity with an untrusted signature")
	}
}

func TestMDMUpdateConcurrentAndFailedInstallRecovery(t *testing.T) {
	f := newMDMUpdateFixture(t, mdmTestOldVersion)
	installed := make(chan struct{})
	finish := make(chan struct{})
	f.install = func() error {
		err := os.WriteFile(f.stage, []byte(mdmTestVersion), 0755)
		close(installed)
		<-finish
		return errors.Join(err, errors.New("simulated Installer failure after staging"))
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.updater.update(t.Context(), "")
		result <- err
	}()
	select {
	case <-installed:
	case err := <-result:
		t.Fatalf("update returned before staging: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("installer did not stage the runtime")
	}
	_, busy := f.updater.update(t.Context(), "")
	close(finish)
	failed := <-result
	if busy == nil || !strings.Contains(busy.Error(), "another managed Doomer update is running") {
		t.Fatalf("concurrent update = %v", busy)
	}
	if failed == nil || !strings.Contains(failed.Error(), "installation failed") {
		t.Fatalf("failed update = %v", failed)
	}
	f.assertActive(t, mdmTestOldVersion)
	if _, err := os.Stat(f.stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed staged runtime was retained: %v", err)
	}
	if got, next := mdmNextCheck(failed, time.Minute, time.Hour); got != time.Minute || next != 2*time.Minute {
		t.Fatalf("retry delay = %v, next backoff = %v", got, next)
	}
	f.install = func() error { return os.WriteFile(f.stage, []byte(mdmTestVersion), 0755) }
	if _, err := f.updater.update(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	f.assertActive(t, mdmTestVersion)
	if got, next := mdmNextCheck(nil, 2*time.Minute, time.Hour); got != 7*time.Hour || next != time.Minute {
		t.Fatalf("normal delay = %v, reset backoff = %v", got, next)
	}
}

func TestMDMUpdateRequiresEnrolledDeviceWithBootstrapAndProfile(t *testing.T) {
	for _, tc := range []struct {
		name, enrollment, problem string
		receiptErr, profileErr    error
	}{
		{name: "bootstrap still installing", problem: "bootstrap installation", receiptErr: os.ErrNotExist},
		{name: "unmanaged", enrollment: "Enrolled via DEP: No\nMDM enrollment: No\n", problem: "active macOS MDM enrollment"},
		{name: "no Doomer profile", enrollment: "MDM enrollment: Yes (User Approved)\n", problem: "device-scoped Doomer MDM profile", profileErr: os.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMDMUpdateFixture(t, "")
			run := func(_ context.Context, path string, _ ...string) ([]byte, error) {
				switch path {
				case mdmPkgutil:
					return nil, tc.receiptErr
				case mdmProfiles:
					return []byte(tc.enrollment), nil
				default:
					t.Fatalf("unexpected command on ineligible Mac: %s", path)
					return nil, nil
				}
			}
			f.updater.readChannel = func(ctx context.Context) (string, error) {
				return mdmDeviceChannel(ctx, run, func(string, bool) error { return tc.profileErr }, mdmPreferences)
			}
			_, err := f.updater.update(t.Context(), "")
			if err == nil || !strings.Contains(err.Error(), tc.problem) || f.requests != 0 || f.installs != 0 {
				t.Fatalf("update attempted on ineligible device: %v, requests=%d, installs=%d", err, f.requests, f.installs)
			}
		})
	}
	for _, tc := range []struct{ profile, channel string }{
		{`{}`, "beta"},
		{`{"UpdateChannel":"stable"}`, "stable"},
		{`{"UpdateChannel":"beta"}`, "beta"},
		{`{"UpdateChannel":"manual"}`, "manual"},
	} {
		if got, err := mdmProfileChannel([]byte(tc.profile)); err != nil || got != tc.channel {
			t.Fatalf("profile %s resolved to %s, %v", tc.profile, got, err)
		}
	}
}
