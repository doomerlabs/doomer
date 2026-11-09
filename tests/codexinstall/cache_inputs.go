package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
)

var installerURLPattern = regexp.MustCompile(`https://raw\.githubusercontent\.com/doomerlabs/skills/[a-f0-9]{40}/scripts/install\.py`)
var guideDigestPattern = regexp.MustCompile("`([a-f0-9]{64})`")

func fetchInput(ctx context.Context, address string) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return nil, fmt.Errorf("input requires HTTPS")
	}
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Cache-Control", "no-cache")
	client := http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 5 {
			return fmt.Errorf("unsafe input redirect")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("input HTTP %d: %s", response.StatusCode, address)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("input too large")
	}
	return data, nil
}

func pinInstaller(data, stable []byte) ([]byte, error) {
	const line = "channel = json.loads(download(STABLE_CHANNEL, 1024 * 1024))"
	if strings.Count(string(data), line) != 1 {
		return nil, fmt.Errorf("installer does not support the known test-only baseline pin")
	}
	return []byte(strings.Replace(string(data), line, "channel = json.loads("+strconv.Quote(string(stable))+")", 1)), nil
}

func freezeExternalInputs(ctx context.Context, dir string, opts snapshotOptions, m *snapshotMetadata) (string, error) {
	capture := func(address string) ([]byte, error) {
		data, err := fetchInput(ctx, address)
		if err == nil {
			m.External = append(m.External, externalInput{address, byteHash(data)})
		}
		return data, err
	}
	var stable []byte
	var err error
	version := opts.Settings["candidate-release"]
	if version != "" {
		manifest, fetchErr := capture("https://downloads.doomer.ai/cli/releases/" + version + "/release-manifest.json")
		if fetchErr != nil {
			return "", fetchErr
		}
		stable, err = channelFromManifest(manifest, version)
		if err != nil {
			return "", err
		}
	} else {
		stable, err = capture("https://downloads.doomer.ai/cli/stable.json")
		if err != nil {
			return "", err
		}
	}
	var channel struct {
		Version string `json:"version"`
		Schema  int    `json:"schema_version"`
	}
	if json.Unmarshal(stable, &channel) != nil || channel.Schema != 1 || !regexp.MustCompile(`^20\d{2}\.\d{1,2}\.\d{1,2}\.\d+$`).MatchString(channel.Version) {
		return "", fmt.Errorf("invalid stable baseline")
	}
	m.Baseline = channel.Version
	m.Stable = stable
	catalog, err := capture(managed.CatalogURL)
	if err != nil {
		return "", err
	}
	if _, err = managed.VerifyCatalog(catalog, managed.ReleasePublicKey, time.Now()); err != nil {
		return "", err
	}
	// Published historical runtimes are immutable, but include their exact manifests.
	for _, version := range []string{"2026.11.6.9"} {
		if _, err = capture("https://downloads.doomer.ai/cli/releases/" + version + "/release-manifest.json"); err != nil {
			return "", err
		}
	}
	if opts.Skills != "" {
		raw, err := os.ReadFile(filepath.Join(opts.Skills, "scripts/install.py"))
		if err != nil {
			return "", err
		}
		if _, err = pinInstaller(raw, stable); err != nil {
			return "", err
		}
		sort.Slice(m.External, func(i, j int) bool { return m.External[i].URL < m.External[j].URL })
		return opts.Guide, nil
	}
	address := opts.Guide
	if address == "" {
		address = "https://doomer.ai/install.md"
	}
	var guide []byte
	if u, _ := url.Parse(address); u != nil && u.Scheme == "file" {
		guide, err = os.ReadFile(u.Path)
		if err == nil {
			m.External = append(m.External, externalInput{"local-install-guide", byteHash(guide)})
		}
	} else {
		guide, err = capture(address)
	}
	if err != nil {
		return "", err
	}
	matches := installerURLPattern.FindAllString(string(guide), -1)
	digests := guideDigestPattern.FindAllStringSubmatch(string(guide), -1)
	if len(matches) != 1 || len(digests) != 1 {
		return "", fmt.Errorf("guide must identify one immutable installer URL and digest")
	}
	installer, err := capture(matches[0])
	if err != nil {
		return "", err
	}
	if byteHash(installer) != digests[0][1] {
		return "", fmt.Errorf("installer checksum mismatch")
	}
	installer, err = pinInstaller(installer, stable)
	if err != nil {
		return "", err
	}
	script := filepath.Join(dir, "pinned-install.py")
	if err = os.WriteFile(script, installer, 0400); err != nil {
		return "", err
	}
	frozen := strings.Replace(string(guide), matches[0], (&url.URL{Scheme: "file", Path: script}).String(), 1)
	frozen = strings.Replace(frozen, digests[0][0], "`"+byteHash(installer)+"`", 1)
	guidePath := filepath.Join(dir, "pinned-install.md")
	if err = os.WriteFile(guidePath, []byte(frozen), 0400); err != nil {
		return "", err
	}
	sort.Slice(m.External, func(i, j int) bool { return m.External[i].URL < m.External[j].URL })
	return (&url.URL{Scheme: "file", Path: guidePath}).String(), nil
}

// The live signed catalog cannot be injected into the released CLI. Give such
// inputs a six-hour freshness limit and reject publication if they changed
// during execution. The stable installer itself uses the captured baseline.
func unchangedExternalInputs(ctx context.Context, m snapshotMetadata) error {
	for _, input := range m.External {
		if input.URL == "local-install-guide" {
			continue
		}
		raw, err := fetchInput(ctx, input.URL)
		if err != nil {
			return err
		}
		if byteHash(raw) != input.SHA256 {
			return fmt.Errorf("external input changed during execution: %s", input.URL)
		}
	}
	return nil
}

// Pin the installer to an immutable release selected before the suite starts,
// even when the moving stable channel changes while jobs are queued.
func channelFromManifest(raw []byte, version string) ([]byte, error) {
	var manifest struct {
		Schema    int               `json:"schemaVersion"`
		Version   string            `json:"version"`
		Artifacts map[string]string `json:"artifacts"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Schema != 1 || manifest.Version != version {
		return nil, fmt.Errorf("invalid pinned release manifest")
	}
	assets := map[string]map[string]string{}
	for _, platform := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		name := "doomer_" + version + "_" + platform + ".tar.gz"
		digest := strings.TrimPrefix(manifest.Artifacts[name], "sha256:")
		if !digestPattern.MatchString(digest) {
			return nil, fmt.Errorf("missing pinned release artifact: %s", name)
		}
		assets[platform] = map[string]string{"url": "https://downloads.doomer.ai/cli/releases/" + version + "/" + name, "sha256": digest}
	}
	return json.Marshal(map[string]any{"schema_version": 1, "version": version, "assets": assets})
}
