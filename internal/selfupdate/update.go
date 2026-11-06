// Package selfupdate manages only the private CLI installed by doomer-review.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
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
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
)

const binaryPath = ".local/bin/doomer"
const recordPath = ".local/share/doomer/installation.json"
const maxBinary = 64 << 20
const maxArchive = 32 << 20

var stableVersion = regexp.MustCompile(`^20[0-9]{2}\.[1-9][0-9]?\.[1-9][0-9]?\.[1-9][0-9]*$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Record struct {
	SchemaVersion int    `json:"schema_version"`
	Manager       string `json:"manager"`
	Path          string `json:"path"`
	Version       string `json:"version"`
	SHA256        string `json:"sha256"`
}
type Status struct {
	State           string `json:"state"`
	Path            string `json:"path"`
	Managed         bool   `json:"managed"`
	Version         string `json:"version,omitempty"`
	RequiredVersion string `json:"required_version"`
	SHA256          string `json:"sha256,omitempty"`
	Backup          string `json:"backup,omitempty"`
	LauncherVersion string `json:"launcher_version,omitempty"`
}
type Updater struct {
	Versioned bool // Install immutable releases side by side.
	Home      string
	HTTP      *http.Client
	BaseURL   string // Injected by tests; the CLI always uses the public download service.
	OS, Arch  string
}

func compare(a, b string) (int, error) {
	if !stableVersion.MatchString(a) || !stableVersion.MatchString(b) {
		return 0, errors.New("expected a stable release version YYYY.M.D.N")
	}
	for i, x := range strings.Split(a, ".") {
		ax, e := strconv.ParseUint(x, 10, 64)
		if e != nil {
			return 0, e
		}
		bx, e := strconv.ParseUint(strings.Split(b, ".")[i], 10, 64)
		if e != nil {
			return 0, e
		}
		if ax < bx {
			return -1, nil
		}
		if ax > bx {
			return 1, nil
		}
	}
	return 0, nil
}
func (u Updater) installPaths(required string) (string, string) {
	if u.Versioned {
		dir := filepath.Join(".local/share/doomer/versions", required)
		return filepath.Join(dir, "doomer"), filepath.Join(dir, "installation.json")
	}
	return binaryPath, recordPath
}

func (u Updater) root(create bool) (*os.Root, error) {
	r, err := os.OpenRoot(u.Home)
	if err != nil {
		return nil, err
	}
	for _, path := range []string{".local", ".local/bin", ".local/share", ".local/share/doomer"} {
		st, e := r.Lstat(path)
		if errors.Is(e, os.ErrNotExist) {
			if create {
				e = r.Mkdir(path, 0700)
			}
			if e == nil || !create {
				continue
			}
		}
		if e != nil {
			r.Close()
			return nil, e
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			r.Close()
			return nil, fmt.Errorf("refusing non-directory or symlink parent %s", path)
		}
	}
	return r, nil
}
func hash(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
func readRegular(r *os.Root, path string, limit int64) ([]byte, error) {
	st, err := r.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > limit {
		return nil, fmt.Errorf("not a bounded regular file: %s", path)
	}
	f, err := r.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(st, actual) {
		return nil, errors.New("installation changed while reading")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("installation exceeds size limit")
	}
	return data, nil
}

// InstallationStamp is a cheap cache key, not a substitute for Check. Hash
// the small record so an update is detected even on coarse-clock filesystems.
func (u Updater) InstallationStamp() (string, error) {
	r, err := u.root(false)
	if err != nil {
		return "", err
	}
	defer r.Close()
	binary := "missing"
	info, err := r.Lstat(binaryPath)
	if err == nil {
		binary = fmt.Sprintf("%d:%d:%d", info.Size(), info.Mode(), info.ModTime().UnixNano())
	} else if !os.IsNotExist(err) {
		return "", err
	}
	record, err := readRegular(r, recordPath, 16<<10)
	if os.IsNotExist(err) {
		return binary + ":missing", nil
	}
	if err != nil {
		return "", err
	}
	return binary + ":" + hash(record), nil
}

func (u Updater) status(r *os.Root, required string) (Status, error) {
	binaryPath, recordPath := u.installPaths(required)
	s := Status{Path: filepath.Join(u.Home, filepath.FromSlash(binaryPath)), RequiredVersion: required, State: "missing"}
	if _, err := compare(required, required); err != nil {
		return s, err
	}
	st, err := r.Lstat(binaryPath)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		s.State = "symlink"
		return s, nil
	}
	if !st.Mode().IsRegular() {
		s.State = "unsupported"
		return s, nil
	}
	data, err := readRegular(r, binaryPath, maxBinary)
	if err != nil {
		return s, err
	}
	s.SHA256 = hash(data)
	s.State = "unmanaged"
	raw, err := readRegular(r, recordPath, 16<<10)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var rec Record
	if json.Unmarshal(raw, &rec) != nil || rec.SchemaVersion != 1 || rec.Manager != "doomer-review" || rec.Path != s.Path || !shaPattern.MatchString(rec.SHA256) {
		return s, nil
	}
	order, err := compare(rec.Version, required)
	if err != nil {
		return s, nil
	}
	if rec.SHA256 != s.SHA256 {
		s.State = "modified"
		return s, nil
	}
	s.Managed = true
	s.Version = rec.Version
	switch {
	case order == 0:
		s.State = "ready"
	case order < 0:
		s.State = "update_available"
	default:
		s.State = "newer_installed"
	}
	return s, nil
}
func (u Updater) Check(required string) (Status, error) {
	r, err := u.root(false)
	if err != nil {
		return Status{}, err
	}
	defer r.Close()
	return u.status(r, required)
}

func (u Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Doomer-CLI-Updater")
	client := http.Client{Timeout: 2 * time.Minute}
	if u.HTTP != nil {
		client = *u.HTTP
	}
	// Release assets stay on the configured download origin; do not follow a
	// manifest or redirect to a different service.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release download exceeds size limit")
	}
	return data, nil
}
func (u Updater) download(ctx context.Context, version string) ([]byte, error) {
	if _, err := compare(version, version); err != nil {
		return nil, err
	}
	base := u.BaseURL
	if base == "" {
		base = "https://downloads.doomer.ai/cli"
	}
	osName, arch := u.OS, u.Arch
	if osName == "" {
		osName = runtime.GOOS
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	if (osName != "darwin" && osName != "linux") || (arch != "amd64" && arch != "arm64") {
		return nil, errors.New("managed updates support macOS and Linux on amd64 and arm64")
	}
	url := strings.TrimSuffix(base, "/") + "/releases/" + version + "/"
	raw, err := u.get(ctx, url+"release-manifest.json", 1<<20)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		SchemaVersion int               `json:"schemaVersion"`
		Version       string            `json:"version"`
		Artifacts     map[string]string `json:"artifacts"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 1 || manifest.Version != version {
		return nil, errors.New("invalid release manifest")
	}
	name := "doomer_" + version + "_" + osName + "_" + arch + ".tar.gz"
	expected := strings.TrimPrefix(manifest.Artifacts[name], "sha256:")
	if !strings.HasPrefix(manifest.Artifacts[name], "sha256:") || !shaPattern.MatchString(expected) {
		return nil, errors.New("release manifest has no valid archive checksum")
	}
	data, err := u.get(ctx, url+name+"?download=1", maxArchive)
	if err != nil {
		return nil, err
	}
	if hash(data) != expected {
		return nil, errors.New("release archive checksum mismatch")
	}
	zip, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zip.Close()
	tarReader := tar.NewReader(io.LimitReader(zip, 128<<20))
	var binary []byte
	for {
		h, e := tarReader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if h.Name != "./doomer" && h.Name != "doomer" {
			continue
		}
		if binary != nil || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > maxBinary || h.Mode&0111 == 0 {
			return nil, errors.New("invalid release executable")
		}
		binary, e = io.ReadAll(io.LimitReader(tarReader, maxBinary+1))
		if e != nil {
			return nil, e
		}
	}
	if len(binary) == 0 {
		return nil, errors.New("release archive has no executable")
	}
	return binary, nil
}
func writeNew(r *os.Root, path string, data []byte, mode os.FileMode) error {
	f, err := r.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	return errors.Join(err, closeErr)
}

// Apply requires the invoking agent to obtain update approval first. Unknown
// installations require explicit adoption; newer managed versions never regress.
func (u Updater) Apply(ctx context.Context, required string, adopt bool) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	before, err := u.Check(required)
	if err != nil {
		return before, err
	}
	if before.State == "ready" {
		return before, nil
	}
	if u.Versioned && before.State != "missing" {
		return before, errors.New("versioned release already exists and failed verification; retain it for inspection")
	}
	if before.State == "newer_installed" {
		return before, errors.New("refusing to downgrade a newer managed CLI")
	}
	if before.State == "unsupported" {
		return before, errors.New("installation path is not a file or symlink")
	}
	if !before.Managed && before.State != "missing" && !adopt {
		return before, errors.New("installation is not managed; obtain approval and use --adopt")
	}
	binary, err := u.download(ctx, required)
	if err != nil {
		return before, err
	}
	r, err := u.root(true)
	if err != nil {
		return before, err
	}
	defer r.Close()
	binaryPath, recordPath := u.installPaths(required)
	if u.Versioned {
		for _, dir := range []string{".local/share/doomer/versions", filepath.Dir(binaryPath)} {
			if err := r.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return before, err
			}
			st, err := r.Lstat(dir)
			if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return before, fmt.Errorf("invalid release directory %s", dir)
			}
		}
	}
	lock, err := publock.TryAcquire(filepath.Join(u.Home, ".local/share/doomer"), "installation")
	if err != nil {
		return before, err
	}
	defer lock.Close()
	if err := ctx.Err(); err != nil {
		return before, err
	}
	current, err := u.status(r, required)
	if err != nil {
		return before, err
	}
	if current.State != before.State || current.SHA256 != before.SHA256 || current.Version != before.Version {
		return current, errors.New("installation changed during update; check it again")
	}
	next := binaryPath + ".next-" + rand.Text()
	recordNext := recordPath + ".next-" + rand.Text()
	defer r.Remove(next)
	defer r.Remove(recordNext)
	if err := writeNew(r, next, binary, 0755); err != nil {
		return before, err
	}
	rec := Record{1, "doomer-review", before.Path, required, hash(binary)}
	record, _ := json.MarshalIndent(rec, "", "  ")
	if err := writeNew(r, recordNext, record, 0600); err != nil {
		return before, err
	}
	backup, err := publishPaths(r, binaryPath, recordPath, next, recordNext, before.State != "missing")
	if err != nil {
		return before, err
	}
	result, err := u.status(r, required)
	if backup != "" {
		result.Backup = filepath.Join(u.Home, filepath.FromSlash(backup))
	}
	return result, err
}

func publish(r *os.Root, next, recordNext string, existing bool) (string, error) {
	return publishPaths(r, binaryPath, recordPath, next, recordNext, existing)
}

func publishPaths(r *os.Root, binaryPath, recordPath, next, recordNext string, existing bool) (string, error) {
	backup := ""
	if existing {
		backup = binaryPath + ".backup-" + rand.Text()
		info, err := r.Lstat(binaryPath)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := r.Readlink(binaryPath)
			if err != nil {
				return "", err
			}
			if err := r.Symlink(target, backup); err != nil {
				return "", err
			}
		} else {
			data, err := readRegular(r, binaryPath, maxBinary)
			if err != nil {
				return "", err
			}
			if err := writeNew(r, backup, data, info.Mode().Perm()); err != nil {
				return "", err
			}
		}
	}
	rollback := func(cause error) (string, error) {
		var restore error
		if backup != "" {
			restore = r.Rename(backup, binaryPath)
		} else {
			restore = r.Remove(binaryPath)
			if errors.Is(restore, os.ErrNotExist) {
				restore = nil
			}
		}
		return "", errors.Join(cause, restore)
	}
	if err := r.Rename(next, binaryPath); err != nil {
		return rollback(err)
	}
	if err := r.Rename(recordNext, recordPath); err != nil {
		return rollback(err)
	}
	return backup, nil
}
