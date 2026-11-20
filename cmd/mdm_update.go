package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/spf13/cobra"
)

const (
	mdmSystemDir        = "/Library/DoomerMDM"
	mdmReleaseOrigin    = "https://downloads.doomer.ai/cli"
	mdmPackageLimit     = 64 << 20
	mdmInstaller        = "/usr/sbin/installer"
	mdmPkgutil          = "/usr/sbin/pkgutil"
	mdmBootstrapReceipt = "com.doomerlabs.doomer.mdm.bootstrap"
	mdmProfiles         = "/usr/bin/profiles"
)

var (
	mdmReleaseVersion    = regexp.MustCompile(`^20[0-9]{2}\.[0-9]{1,2}\.[0-9]{1,2}(\.[1-9][0-9]*)?(-beta\.[0-9]+)?$`)
	mdmSHA256            = regexp.MustCompile(`^[a-f0-9]{64}$`)
	mdmInstallerIdentity = regexp.MustCompile(`^Developer ID Installer: [^\r\n]+ \([A-Z0-9]{10}\)$`)
	errMDMNotFound       = errors.New("download not found")
	mdmEnrolled          = regexp.MustCompile(`(?m)^MDM enrollment: Yes(?: \([^\r\n]*\))?[ \t]*$`)
)

type mdmRetryError struct{ error }

func newMDMUpdateCommand() *cobra.Command {
	var scheduled bool
	var channel string
	c := &cobra.Command{
		Use: "update", Short: "Update the managed macOS runtime", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if channel != "" && (scheduled || channel != "stable" && channel != "beta") {
				return errors.New("use --channel stable or --channel beta for a manual update")
			}
			return runSystemMDMUpdate(c.Context(), c.OutOrStdout(), scheduled, channel)
		},
	}
	c.Flags().BoolVar(&scheduled, "scheduled", false, "run continuously under the system LaunchDaemon")
	_ = c.Flags().MarkHidden("scheduled")
	c.Flags().StringVar(&channel, "channel", "", "select a release channel for a one-time update in manual mode")
	return c
}

type mdmUpdater struct {
	dir, origin string
	client      *http.Client
	readChannel func(context.Context) (string, error)
	run         func(context.Context, string, ...string) ([]byte, error)
	checkPath   func(string, bool) error
}

func (u mdmUpdater) update(ctx context.Context, manualChannel string) (string, error) {
	channel, err := u.readChannel(ctx)
	if err != nil {
		return "", err
	}
	profileChannel := channel
	if channel == "manual" {
		if manualChannel == "" {
			return "Managed Doomer updates are in manual mode.", nil
		}
		channel = manualChannel
	} else if manualChannel != "" {
		return "", errors.New("--channel is only available when UpdateChannel is manual")
	}
	if channel != "stable" && channel != "beta" {
		return "", errors.New("UpdateChannel must be stable, beta, or manual")
	}

	lock, err := publock.TryAcquire(u.dir, "mdm-update")
	if publock.Busy(err) {
		return "", mdmRetryError{errors.New("another managed Doomer update is running")}
	}
	if err != nil {
		return "", fmt.Errorf("lock managed Doomer update: %w", err)
	}
	defer lock.Close()

	var feed struct {
		Schema  int `json:"schema_version"`
		Managed *struct {
			Version        string `json:"version"`
			ManifestSHA256 string `json:"manifest_sha256"`
		} `json:"managed_package"`
	}
	data, err := u.downloadBytes(ctx, u.origin+"/"+channel+".json", 8<<20)
	if errors.Is(err, errMDMNotFound) {
		return mdmMissingPackage(channel), nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s channel: %w", channel, err)
	}
	if json.Unmarshal(data, &feed) != nil || feed.Schema != 1 {
		return "", errors.New("invalid managed Doomer channel feed")
	}
	if feed.Managed == nil {
		return mdmMissingPackage(channel), nil
	}

	target := feed.Managed.Version
	if !mdmReleaseVersion.MatchString(target) || !mdmSHA256.MatchString(feed.Managed.ManifestSHA256) ||
		strings.Contains(target, "-beta.") != (channel == "beta") {
		return "", errors.New("invalid managed package pointer")
	}
	active := filepath.Join(u.dir, "doomer")
	currentPath := active
	info, err := os.Lstat(active)
	switch {
	case errors.Is(err, os.ErrNotExist):
		currentPath = filepath.Join(u.dir, "bootstrap-doomer")
	case err != nil:
		return "", err
	case info.Mode().IsRegular() && info.Mode().Perm()&0111 == 0:
		currentPath = filepath.Join(u.dir, "bootstrap-doomer")
	}
	current, err := u.runtimeVersion(ctx, currentPath)
	if err != nil {
		return "", fmt.Errorf("read current managed Doomer version: %w", err)
	}
	if current == target {
		return "Managed Doomer is already current at " + current + ".", nil
	}
	if !mdmVersionLess(current, target) {
		return fmt.Sprintf("Managed Doomer is at %s. The %s package %s would downgrade it.", current, channel, target), nil
	}

	releaseURL := u.origin + "/releases/" + target + "/"
	data, err = u.downloadBytes(ctx, releaseURL+"mdm-release.json", 4096)
	if err != nil {
		return "", fmt.Errorf("download managed checksum manifest: %w", err)
	}
	var manifest struct {
		Schema        int    `json:"schemaVersion"`
		PackageSHA256 string `json:"packageSHA256"`
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != feed.Managed.ManifestSHA256 ||
		json.Unmarshal(data, &manifest) != nil || manifest.Schema != 2 || !mdmSHA256.MatchString(manifest.PackageSHA256) {
		return "", errors.New("invalid managed package checksum manifest")
	}
	if err := u.install(ctx, releaseURL+"Doomer-MDM-"+target+".pkg", manifest.PackageSHA256, target, active, profileChannel); err != nil {
		return "", err
	}
	return "Installed managed Doomer " + target + " from the " + channel + " channel.", nil
}

// macOS Installer writes the bootstrap receipt after postinstall succeeds.
func mdmDeviceChannel(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), checkPath func(string, bool) error, preferences string) (string, error) {
	if _, err := run(ctx, mdmPkgutil, "--pkg-info", mdmBootstrapReceipt); err != nil {
		return "", mdmRetryError{errors.New("waiting for the managed Doomer bootstrap installation to finish")}
	}
	enrollment, err := run(ctx, mdmProfiles, "status", "-type", "enrollment")
	if err != nil {
		return "", mdmRetryError{errors.New("unable to confirm macOS MDM enrollment")}
	}
	if !mdmEnrolled.Match(enrollment) {
		return "", errors.New("managed Doomer updates require active macOS MDM enrollment")
	}
	if err := checkPath(filepath.Dir(preferences), true); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", mdmRetryError{errors.New("waiting for the device-scoped Doomer MDM profile")}
		}
		return "", err
	}
	if err := checkPath(preferences, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", mdmRetryError{errors.New("waiting for the device-scoped Doomer MDM profile")}
		}
		return "", err
	}
	data, err := run(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", preferences)
	if err != nil {
		return "", mdmRetryError{errors.New("unable to read the device-scoped Doomer MDM profile")}
	}
	return mdmProfileChannel(data)
}

func mdmProfileChannel(data []byte) (string, error) {
	var profile struct {
		UpdateChannel string `json:"UpdateChannel"`
	}
	if len(data) > 16<<10 || json.Unmarshal(data, &profile) != nil {
		return "", errors.New("invalid device-scoped Doomer MDM profile")
	}
	if profile.UpdateChannel == "" {
		return "beta", nil
	}
	return profile.UpdateChannel, nil
}

func mdmMissingPackage(channel string) string {
	return "The " + channel + " channel has no managed Doomer package. The current runtime was kept."
}

func (u mdmUpdater) install(ctx context.Context, url, checksum, target, active, profileChannel string) error {
	work, err := os.MkdirTemp(u.dir, "download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	pkg := filepath.Join(work, "doomer.pkg")
	file, err := os.OpenFile(pkg, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	downloadErr := u.download(ctx, url, io.MultiWriter(file, hash), mdmPackageLimit)
	closeErr := file.Close()
	if err := errors.Join(downloadErr, closeErr); err != nil {
		return fmt.Errorf("download managed package: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != checksum {
		return errors.New("managed package SHA-256 mismatch")
	}

	signerPath := filepath.Join(u.dir, "installer-identity")
	if err := u.checkPath(signerPath, false); err != nil {
		return err
	}
	identity, err := os.ReadFile(signerPath)
	if err != nil {
		return err
	}
	signature, err := u.run(ctx, mdmPkgutil, "--check-signature", pkg)
	if err != nil || !mdmTrustedSigner(signature, strings.TrimSpace(string(identity))) {
		return errors.New("managed package does not have the expected trusted Doomer Installer signature")
	}

	// A long download may overlap MDM unenrollment or a policy change.
	channel, err := u.readChannel(ctx)
	if err != nil {
		return err
	}
	if channel != profileChannel {
		return mdmRetryError{errors.New("UpdateChannel changed while downloading the managed package")}
	}

	incoming := filepath.Join(u.dir, "incoming")
	if err := u.checkPath(incoming, true); err != nil {
		return err
	}
	candidate := filepath.Join(incoming, "doomer")
	if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear staged runtime: %w", err)
	}
	defer os.Remove(candidate)
	if _, err := u.run(ctx, mdmInstaller, "-pkg", pkg, "-target", "/"); err != nil {
		return mdmRetryError{fmt.Errorf("managed package installation failed: %w", err)}
	}
	if got, err := u.runtimeVersion(ctx, candidate); err != nil || got != target {
		return errors.New("installed managed runtime did not report the expected version")
	}
	if err := os.Rename(candidate, active); err != nil {
		return fmt.Errorf("activate managed runtime: %w", err)
	}
	return nil
}

func (u mdmUpdater) runtimeVersion(ctx context.Context, path string) (string, error) {
	if err := u.checkPath(path, false); err != nil {
		return "", err
	}
	data, err := u.run(ctx, path, "version", "--format", "json")
	if err != nil {
		return "", err
	}
	var result struct{ Version string }
	if json.Unmarshal(data, &result) != nil || !mdmReleaseVersion.MatchString(result.Version) {
		return "", errors.New("invalid managed runtime version")
	}
	return result.Version, nil
}

func mdmTrustedSigner(output []byte, identity string) bool {
	if !mdmInstallerIdentity.MatchString(identity) {
		return false
	}
	trusted := false
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Status:") {
			trusted = strings.HasPrefix(line, "Status: signed by a certificate trusted by ") ||
				line == "Status: signed by a developer certificate issued by Apple for distribution"
		}
		if strings.HasPrefix(line, "1. ") {
			return trusted && line == "1. "+identity
		}
	}
	return false
}

func mdmVersionLess(a, b string) bool {
	key := func(version string) [6]uint64 {
		var parts [6]uint64
		main, beta, prerelease := strings.Cut(version, "-beta.")
		for i, field := range strings.Split(main, ".") {
			parts[i], _ = strconv.ParseUint(field, 10, 64)
		}
		if prerelease {
			parts[5], _ = strconv.ParseUint(beta, 10, 64)
		} else {
			parts[4] = 1
		}
		return parts
	}
	left, right := key(a), key(b)
	for i := range left {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return false
}

func (u mdmUpdater) downloadBytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	var data strings.Builder
	if err := u.download(ctx, url, &data, limit); err != nil {
		return nil, err
	}
	return []byte(data.String()), nil
}

func (u mdmUpdater) download(ctx context.Context, url string, out io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return mdmRetryError{err}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return mdmRetryError{errMDMNotFound}
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return mdmRetryError{fmt.Errorf("download returned HTTP %d", resp.StatusCode)}
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	size, err := io.Copy(out, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return mdmRetryError{err}
	}
	if size > limit {
		return errors.New("managed download exceeds size limit")
	}
	return nil
}

func (e mdmRetryError) Unwrap() error { return e.error }

func mdmRuntimeReplaced(active string, running os.FileInfo) bool {
	info, err := os.Lstat(active)
	return err == nil && info.Mode().IsRegular() && !os.SameFile(running, info)
}

func mdmNextCheck(err error, previous, jitter time.Duration) (delay, backoff time.Duration) {
	var retry mdmRetryError
	if errors.As(err, &retry) {
		return previous, min(2*previous, 30*time.Minute)
	}
	return 6*time.Hour + jitter, time.Minute
}
