package watch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const hookPingTimeout = 500 * time.Millisecond
const hookReadyTTL = 2 * time.Second
const hookFailureCooldown = 10 * time.Second

type hookServiceState struct {
	API        string
	AccountKey string
	Profile    string
	CheckedAt  time.Time
	Ready      bool
}

func watcherAlive(root string) bool {
	var heartbeat time.Time
	return Read(filepath.Join(root, "heartbeat.json"), &heartbeat) == nil && time.Since(heartbeat) < 5*time.Second
}

func cachedHookReadiness(root string, cfg Config) (bool, bool) {
	var state hookServiceState
	if Read(filepath.Join(root, "hook-service.json"), &state) != nil || state.API != cfg.API || state.AccountKey != cfg.AccountKey || state.Profile != cfg.Profile {
		return false, false
	}
	ttl := hookFailureCooldown
	if state.Ready {
		ttl = hookReadyTTL
	}
	age := time.Since(state.CheckedAt)
	return state.Ready, age >= 0 && age < ttl
}

func recordHookReadiness(root string, cfg Config, ready bool) {
	_ = Write(filepath.Join(root, "hook-service.json"), hookServiceState{API: cfg.API, AccountKey: cfg.AccountKey, Profile: cfg.Profile, CheckedAt: time.Now(), Ready: ready})
}

// The public health endpoint needs no token or source data. This only gates
// waiting; normal requests still use the selected profile's authentication.
func hookServiceReady(ctx context.Context, root string, cfg Config) bool {
	if ready, cached := cachedHookReadiness(root, cfg); cached {
		return ready
	}
	ready := false
	// A simultaneous hook owns the probe. Let this hook finish rather than
	// waiting for another process or making a duplicate health request.
	_ = Locked(root, "hook-service", func() error {
		if value, cached := cachedHookReadiness(root, cfg); cached {
			ready = value
			return nil
		}
		ready = pingHookService(ctx, cfg.API)
		recordHookReadiness(root, cfg, ready)
		return nil
	})
	return ready
}

func pingHookService(ctx context.Context, api string) bool {
	endpoint, err := url.Parse(strings.TrimSuffix(strings.TrimRight(api, "/"), "/api") + "/healthz")
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, hookPingTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: hookPingTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var health struct {
		OK bool `json:"ok"`
	}
	return response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&health) == nil && health.OK
}
