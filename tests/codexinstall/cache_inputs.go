package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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

func freezeExternalInputs(ctx context.Context, dir string, opts snapshotOptions, m *snapshotMetadata) (string, error) {
	if opts.Node == "" || opts.Skills == "" {
		return "", fmt.Errorf("new onboarding requires captured Node manager and skills sources")
	}
	version := opts.Settings["skills-cli-version"]
	if version == "" {
		return "", fmt.Errorf("pin --skills-cli-version")
	}
	address := "https://registry.npmjs.org/skills/" + url.PathEscape(version)
	raw, err := fetchInput(ctx, address)
	if err != nil {
		return "", err
	}
	var pkg struct {
		Version string `json:"version"`
		Dist    struct {
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if json.Unmarshal(raw, &pkg) != nil || pkg.Version != version || pkg.Dist.Integrity == "" {
		return "", fmt.Errorf("invalid pinned skills package")
	}
	m.External = append(m.External, externalInput{URL: address, SHA256: byteHash(raw)})
	m.Baseline = opts.Settings["candidate-release"]
	tooling := filepath.Join(dir, "tooling")
	if err = os.MkdirAll(tooling, 0700); err != nil {
		return "", err
	}
	packageJSON, _ := json.Marshal(map[string]any{"private": true, "dependencies": map[string]string{"skills": version}})
	if err = os.WriteFile(filepath.Join(tooling, "package.json"), packageJSON, 0600); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "npm", "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund", "--registry=https://registry.npmjs.org")
	cmd.Dir = tooling
	cmd.Env = append(os.Environ(), "NPM_CONFIG_USERCONFIG="+os.DevNull)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("freeze skills dependencies: %w: %s", err, output)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		data, err := os.ReadFile(filepath.Join(tooling, name))
		if err != nil {
			return "", err
		}
		m.Files = append(m.Files, snapshotEntry{Path: "tooling/" + name, SHA256: byteHash(data), Mode: 0600})
	}
	for _, tool := range []string{"node", "npm"} {
		output, err := exec.CommandContext(ctx, tool, "--version").Output()
		if err != nil {
			return "", err
		}
		m.Settings[tool+"_version"] = strings.TrimSpace(string(output))
	}
	return "", nil
}

func unchangedExternalInputs(ctx context.Context, m snapshotMetadata) error {
	for _, input := range m.External {
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
