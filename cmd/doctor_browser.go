package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func doctorBrowserEndpoint(origin, repo string, allowUnconfigured bool) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || u.Opaque != "" ||
		(u.Scheme != "https" && (u.Scheme != "http" || !isDoctorLoopback(u.Hostname()))) {
		return "", fmt.Errorf("copy a fresh Doctor command from the troubleshooting page")
	}
	public, _ := url.Parse(adversarylabs.DefaultAPIURL)
	if !allowUnconfigured && (u.Scheme != public.Scheme || u.Host != public.Host) {
		api, err := configuredDoctorBrowserAPI(repo)
		if err != nil {
			return "", err
		}
		configured, err := url.Parse(api)
		if err != nil || u.Scheme != configured.Scheme || u.Host != configured.Host {
			return "", fmt.Errorf("browser origin %q does not match your configured Doomer service. For a dev site you trust, rerun with --allow-unconfigured-browser-origin", u.String())
		}
	}
	u.Path = "/api/docs/doctor-session"
	return u.String(), nil
}

func configuredDoctorBrowserAPI(repo string) (string, error) {
	var state agentHookState
	if home, err := defaultManagerHome(); err == nil {
		_ = watching.Read(filepath.Join(home, "state.json"), &state)
	}
	if state.Managed != nil {
		return agentHookAPI(state.API, "", "")
	}
	env, err := directoryWatchEnvironment(repo)
	if err != nil {
		return "", err
	}
	return agentHookAPI("", env.get("DOOMER_API_URL"), state.API)
}

func isDoctorLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

func browserHookChecks(report map[string]any) []string {
	checks := []string{"hook_configuration_checked"}
	configs, _ := report["hook_configs"].([]any)
	for _, config := range configs {
		entry, _ := config.(map[string]any)
		agent, _ := entry["active_profile_agent"].(string)
		if agent != "codex" && agent != "claude" {
			continue
		}
		switch entry["installation"] {
		case "installed":
			checks = append(checks, agent+"_hooks_installed")
		case "missing":
			checks = append(checks, agent+"_hooks_missing")
		}
	}
	return checks
}

func sendDoctorBrowserResult(ctx context.Context, endpoint, code string, checks []string) error {
	payload, err := json.Marshal(map[string]any{
		"action": "submit",
		"code":   code,
		"result": map[string]any{"source": "native", "checks": checks},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn’t reach Doomer Doctor. Paste the terminal result into the page")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Doomer Doctor didn’t accept the result (HTTP %d). Copy a fresh command or paste the terminal result", resp.StatusCode)
	}
	var receipt struct {
		Received bool `json:"received"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1024)).Decode(&receipt); err != nil || !receipt.Received {
		return fmt.Errorf("Doomer Doctor didn’t confirm receipt. Paste the terminal result into the page")
	}
	return nil
}
