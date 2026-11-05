package cmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

type setupGrantRequest struct {
	setupConsent
	Callback            string `json:"callback"`
	CodeChallenge       string `json:"codeChallenge"`
	ActivationReporting bool   `json:"activationReporting"`
}

// Negotiate reporting support without changing the approval flow.
func (s *setupGrantRequest) SetActivationReporting(enabled bool) {
	if s.setupConsent.ActivationReporting != nil {
		*s.setupConsent.ActivationReporting = enabled
	}
}

type setupBrowserClient interface {
	CreateSetupGrant(context.Context, string, any) (string, error)
	ExchangeCode(context.Context, string, string, string) (adversarylabs.TokenResponse, error)
}

// The website sees its own login cookie. Only a one-time PKCE code comes back
// through the loopback callback; the credential is exchanged over the API.
func hostedSetupConsent(ctx context.Context, s setupConsent, credential string, client setupBrowserClient, out io.Writer, open func(context.Context, string) error) (bool, *adversarylabs.TokenResponse, error) {
	destination, err := url.Parse(s.API)
	if err != nil || destination.Host == "" || destination.User != nil || (destination.Scheme != "http" && destination.Scheme != "https") {
		return false, nil, fmt.Errorf("invalid profile API URL")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return false, nil, fmt.Errorf("%w: %v", errBrowserLaunch, err)
	}
	defer listener.Close()
	var nonce [32]byte
	var secret [48]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return false, nil, err
	}
	if _, err = rand.Read(secret[:]); err != nil {
		return false, nil, err
	}
	s.Nonce = hex.EncodeToString(nonce[:])
	verifier := base64.RawURLEncoding.EncodeToString(secret[:])
	challenge := sha256.Sum256([]byte(verifier))
	callback := "http://" + listener.Addr().String() + "/complete?nonce=" + s.Nonce
	link, err := client.CreateSetupGrant(ctx, credential, &setupGrantRequest{setupConsent: s, Callback: callback, CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]), ActivationReporting: true})
	if err != nil {
		return false, nil, fmt.Errorf("create browser setup request: %w", err)
	}
	page, err := url.Parse(link)
	if err != nil || page.Scheme != destination.Scheme || page.Host != destination.Host || page.User != nil || page.Path != "/cli/setup" || page.RawQuery != "" || page.Fragment == "" {
		return false, nil, fmt.Errorf("invalid browser setup URL")
	}
	results := make(chan setupGrantResult, 1)
	handler := hostedApprovalHandler(s, listener.Addr().String(), results)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = server.Close()
	}()
	go server.Serve(listener)
	launch, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err = open(launch, link); err != nil {
		return false, nil, fmt.Errorf("%w: %v", errBrowserLaunch, err)
	}
	fmt.Fprintf(out, "Finish setup in your browser for profile %s (%s).\nWaiting for your choice; press Ctrl+C to cancel.\n", s.Profile, s.API)
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	select {
	case result := <-results:
		if !result.approved {
			return false, nil, nil
		}
		if result.code == "" {
			return true, nil, nil
		}
		token, err := client.ExchangeCode(ctx, result.code, verifier, callback)
		if err != nil {
			return false, nil, fmt.Errorf("sign in from setup: %w", err)
		}
		if token.Token == "" {
			return false, nil, fmt.Errorf("setup login response has no credential")
		}
		return true, &token, nil
	case <-ctx.Done():
		return false, nil, ctx.Err()
	case <-timer.C:
		return false, nil, fmt.Errorf("browser setup expired; no approval saved. Run setup again or use --no-browser")
	}
}

type setupGrantResult struct {
	approved bool
	code     string
}

func hostedApprovalHandler(s setupConsent, host string, result chan<- setupGrantResult) http.Handler {
	var mu sync.Mutex
	used := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; style-src 'unsafe-inline'")
		if r.Host != host || r.Method != http.MethodGet || r.URL.Path != "/complete" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("nonce")), []byte(s.Nonce)) != 1 {
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
		allowed := decision == "allow"
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = setupPage.ExecuteTemplate(w, "complete", struct {
			Consent  setupConsent
			Approved bool
		}{s, allowed})
		result <- setupGrantResult{allowed, r.URL.Query().Get("code")}
	})
}
