package cmd

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Only the selected profile's destination and requested scope enter the browser.
// Login credentials stay in the CLI, and approval exists only for this invocation.
type setupConsent struct {
	Profile         string   `json:"profile"`
	API             string   `json:"api"`
	AllRepositories bool     `json:"allRepositories"`
	Directories     []string `json:"directories"`
	Agents          []string `json:"agents"`
	Nonce           string   `json:"nonce"`
}

func (s setupConsent) print(w io.Writer) {
	fmt.Fprintln(w, "Allow Doomer to review this repo?\n\nDoomer sends code from this repo, along with the task prompt and the agent’s review responses, to Doomer’s servers for analysis. You’ll get free initial feedback in your coding chat. No account required.")
	fmt.Fprintf(w, "\nProfile:      %s\nDestination:  %s\nHooks:        %s\n", s.Profile, s.API, strings.Join(s.Agents, ", "))
	if s.AllRepositories {
		fmt.Fprintln(w, "Access:       Repositories your coding agent edits, including future checkouts.")
	} else {
		fmt.Fprintln(w, "Access:       Only these repositories and their linked worktrees:")
		for _, dir := range s.Directories {
			fmt.Fprintln(w, "              "+dir)
		}
	}
	fmt.Fprintln(w, "Sends:        Base and current source snapshots (including files outside your changes), the task prompt behind an agent edit, and agent review responses.\n\nRepositories are discovered through your coding agent; Doomer does not scan your whole disk.\nSetup installs hooks. Uploads begin only while the watcher runs. See the destination’s privacy policy for retention; expiry of a review link does not delete uploaded source.")
}

func setupDesktop(goos string, getenv func(string) string) bool {
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_CLIENT") != "" || getenv("SSH_TTY") != "" {
		return false
	}
	return goos == "darwin" || (goos == "linux" && (getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""))
}

var openSetupBrowser = openBrowser

var errBrowserLaunch = errors.New("could not open setup in a browser")

func setupApprovalHandler(s setupConsent, host string, result chan<- bool) http.Handler {
	var mu sync.Mutex
	used := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; form-action 'self'; style-src 'unsafe-inline'")
		if r.Host != host || r.Method != http.MethodGet || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("nonce")), []byte(s.Nonce)) != 1 {
			http.Error(w, "Invalid setup request.", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/consent" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = setupPage.Execute(w, s)
			return
		}
		if r.URL.Path != "/complete" {
			http.Error(w, "Invalid setup request.", http.StatusForbidden)
			return
		}
		decision := r.URL.Query().Get("decision")
		if decision != "allow" && decision != "cancel" {
			http.Error(w, "Invalid decision.", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if used {
			http.Error(w, "This setup request has already finished.", http.StatusGone)
			return
		}
		used = true
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><title>Doomer setup</title><h1>Return to your terminal</h1><p>Your choice was received. The terminal will show whether setup completed.</p>")
		result <- decision == "allow"
	})
}

func browserSetupConsent(ctx context.Context, s setupConsent, out io.Writer, open func(context.Context, string) error) (bool, error) {
	destination, err := url.Parse(s.API)
	if err != nil || destination.Host == "" || destination.User != nil || (destination.Scheme != "http" && destination.Scheme != "https") {
		return false, fmt.Errorf("invalid profile API URL")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return false, fmt.Errorf("%w: %v", errBrowserLaunch, err)
	}
	defer listener.Close()
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return false, err
	}
	s.Nonce = hex.EncodeToString(nonce)
	link := "http://" + listener.Addr().String() + "/consent?nonce=" + url.QueryEscape(s.Nonce)
	result := make(chan bool, 1)
	server := &http.Server{Handler: setupApprovalHandler(s, listener.Addr().String(), result), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = server.Close()
	}()
	go server.Serve(listener)
	launch, cancelLaunch := context.WithTimeout(ctx, 10*time.Second)
	defer cancelLaunch()
	if err = open(launch, link); err != nil {
		return false, fmt.Errorf("%w: %v", errBrowserLaunch, err)
	}
	fmt.Fprintf(out, "Finish setup in your browser for profile %s (%s).\nWaiting for your choice; press Ctrl+C to cancel.\n", s.Profile, s.API)
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	select {
	case approved := <-result:
		return approved, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return false, fmt.Errorf("browser setup expired; no approval saved. Run setup again or use --no-browser")
	}
}

func defaultSetupBrowser() bool { return setupDesktop(runtime.GOOS, os.Getenv) }

// Standard terminal reads do not observe the command context. Keep the read
// separate so Ctrl+C can end setup without waiting for a newline. main exits
// after cancellation; the CLI does not close the caller's shared stdin.
func terminalSetupConsent(ctx context.Context, input io.Reader) bool {
	if ctx.Err() != nil {
		return false
	}
	answer := make(chan bool, 1)
	go func() {
		var text string
		_, err := fmt.Fscanln(input, &text)
		answer <- err == nil && text == "yes"
	}()
	select {
	case <-ctx.Done():
		return false
	case approved := <-answer:
		return approved && ctx.Err() == nil
	}
}

// This screen is served by the local CLI so its disclosure matches the installed
// version. No repository content or credentials are sent to render it.
var setupPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Allow Doomer to review this repo?</title>
<style>body{font:16px/1.6 system-ui,sans-serif;color:#202020;background:#f6f5f2;margin:0;padding:48px 20px}main{max-width:680px;margin:auto;background:white;padding:32px;border:1px solid #ddd;border-radius:16px}h1{line-height:1.2;font-size:28px}code{overflow-wrap:anywhere}details{border-top:1px solid #ddd;border-bottom:1px solid #ddd;padding:16px 0;margin:24px 0}summary,button{cursor:pointer}button{font:inherit;border:1px solid #ccc;border-radius:8px;padding:10px 18px;background:white;margin:4px}button[value=allow]{background:#222;color:white;border-color:#222}a{color:inherit}</style>
<main>
<h1>Allow Doomer to review this repo?</h1>
<p>Doomer sends code from this repo, along with the task prompt and the agent’s review responses, to Doomer’s servers for analysis. You’ll get free initial feedback in your coding chat. No account required.</p>
<ul>{{range .Directories}}<li><code>{{.}}</code></li>{{end}}</ul>
<p>Destination: <code>{{.API}}</code><br>Profile: <code>{{.Profile}}</code></p>
<details><summary>What gets sent?</summary>
<p>Base and current repository snapshots, including tracked and non-ignored source files outside the changed lines, the task prompt behind an agent edit, and the agent’s review responses. Unchanged files may be reused from previous uploads. Approval also covers this repository’s linked Git worktrees.</p>
<p>Uploaded source is retained under the <a href="{{.PrivacyURL}}" target="_blank" rel="noreferrer">service’s privacy policy</a>. Expiry of a review link does not delete the uploaded source.</p>
<p>Setup installs {{range .Agents}}{{.}} {{end}}hooks. Your coding agent may separately ask you to trust them. The watcher uploads only after approval and an attributable agent edit. You can pause watching or revoke approval from the CLI.</p>
</details>
<form method="get" action="/complete"><input type="hidden" name="nonce" value="{{.Nonce}}">
<button name="decision" value="allow">Allow for this repo</button>
<button name="decision" value="cancel">Not now</button></form>
</main></html>`))

func (s setupConsent) PrivacyURL() string {
	u, err := url.Parse(s.API)
	if err != nil {
		return ""
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "/privacy", "", "", ""
	return u.String()
}
