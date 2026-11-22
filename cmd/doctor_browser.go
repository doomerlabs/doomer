package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

var browserSessionCode = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func doctorBrowserEndpoint(origin, code string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || !browserSessionCode.MatchString(code) || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || u.Opaque != "" ||
		(u.Scheme != "https" && (u.Scheme != "http" || !isDoctorLoopback(u.Hostname()))) {
		return "", fmt.Errorf("copy a fresh Doctor command from the troubleshooting page")
	}
	u.Path = "/api/docs/doctor-session"
	return u.String(), nil
}

func isDoctorLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
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
