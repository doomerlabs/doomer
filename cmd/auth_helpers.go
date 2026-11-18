package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// Device authorization works whether the browser and CLI share a computer or
// not. Use it by default without guessing from the terminal environment, while
// preserving explicit browser, password, token, and CI login choices.
func useDeviceLogin(opts loginOptions, explicitlySet bool) bool {
	if explicitlySet || opts.device || opts.ci || opts.tokenStdin || opts.emailAddress != "" || opts.passwordStdin {
		return opts.device
	}
	return true
}

func waitForLogin(ctx context.Context, clock application.Clock, client application.APIClient, login adversarylabs.DeviceLogin) (adversarylabs.TokenResponse, error) {
	interval := adversarylabs.PollInterval(login)
	expiresAt := clock.Now().Add(time.Duration(login.ExpiresIn) * time.Second)
	if login.ExpiresIn <= 0 {
		expiresAt = clock.Now().Add(10 * time.Minute)
	}
	pollCtx, cancel := context.WithDeadline(ctx, expiresAt)
	defer cancel()
	for {
		if err := pollCtx.Err(); err != nil {
			if ctx.Err() != nil {
				return adversarylabs.TokenResponse{}, ctx.Err()
			}
			return adversarylabs.TokenResponse{}, fmt.Errorf("login expired before authentication completed")
		}
		token, err := client.PollToken(pollCtx, login.DeviceCode)
		if err == nil {
			return token, nil
		}
		if pollCtx.Err() != nil {
			if ctx.Err() != nil {
				return adversarylabs.TokenResponse{}, ctx.Err()
			}
			return adversarylabs.TokenResponse{}, fmt.Errorf("login expired before authentication completed")
		}
		var response *adversarylabs.ResponseError
		if errors.As(err, &response) && response.Code == "access_denied" {
			return adversarylabs.TokenResponse{}, fmt.Errorf("browser authorization declined")
		}
		timer := clock.NewTimer(interval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return adversarylabs.TokenResponse{}, ctx.Err()
			}
			return adversarylabs.TokenResponse{}, fmt.Errorf("login expired before authentication completed")
		case <-timer.C():
		}
	}
}

func loginWithDevice(ctx context.Context, clock application.Clock, stdout io.Writer, client application.APIClient, opts *loginOptions, api string) (adversarylabs.TokenResponse, error) {
	login, err := client.BeginLogin(ctx, adversarylabs.LoginOptions{Name: opts.name, CI: opts.ci, ReviewUploads: opts.reviewUploads})
	if err != nil {
		return adversarylabs.TokenResponse{}, err
	}
	verificationURL := login.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = login.VerificationURI
	}
	if verificationURL == "" || login.UserCode == "" {
		return adversarylabs.TokenResponse{}, fmt.Errorf("device login response was missing verification instructions")
	}
	if opts.reviewUploads {
		if err := validateReviewApprovalURL(api, verificationURL, login.UserCode, os.Getenv("DOOMER_DEV_BROWSER_ORIGIN")); err != nil {
			return adversarylabs.TokenResponse{}, err
		}
		fmt.Fprintf(stdout, "Review permissions in your browser.\n%s\nCode: %s\n", verificationURL, login.UserCode)
		launch, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = openBrowser(launch, verificationURL)
		cancel()
		token, err := waitForLogin(ctx, clock, client, login)
		if err != nil {
			return adversarylabs.TokenResponse{}, err
		}
		if !slices.Contains(strings.Fields(token.Scope), "reviews:upload") {
			return adversarylabs.TokenResponse{}, fmt.Errorf("the service did not confirm review upload approval")
		}
		return token, nil
	}
	fmt.Fprintf(stdout, "Open %s\n\nEnter code: %s\n\nWaiting for authentication...\n", verificationURL, login.UserCode)
	return waitForLogin(ctx, clock, client, login)
}

// Development proxies can serve the same app at a browser-accessible HTTPS
// origin while the CLI uses a loopback API. Trust only an explicit origin.
func validateReviewApprovalURL(api, verificationURL, code, devOrigin string) error {
	page, pageErr := url.Parse(verificationURL)
	service, serviceErr := url.Parse(api)
	if pageErr != nil || serviceErr != nil || page.User != nil || page.Path != "/cli/authorize" ||
		page.Query().Get("code") != code || page.Fragment != "" {
		return fmt.Errorf("unexpected browser approval URL")
	}
	if page.Scheme == service.Scheme && page.Host == service.Host {
		return nil
	}
	host := service.Hostname()
	ip := net.ParseIP(host)
	if devOrigin != "" && (strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())) {
		origin, err := url.Parse(devOrigin)
		if err == nil && origin.Scheme == "https" && origin.Host != "" && origin.User == nil &&
			(origin.Path == "" || origin.Path == "/") && origin.RawQuery == "" && origin.Fragment == "" &&
			page.Scheme == origin.Scheme && page.Host == origin.Host {
			return nil
		}
	}
	return fmt.Errorf("browser approval is on a different origin from the Doomer API")
}

func readPasswordLine(r io.Reader) (string, error) {
	return readSecretLine(r, "password")
}

func readSecretLine(r io.Reader, kind string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 64*1024+1))
	if err != nil {
		return "", err
	}
	if len(data) > 64*1024 {
		return "", fmt.Errorf("%s exceeds 64 KiB limit", kind)
	}
	secret := strings.TrimRight(string(data), "\r\n")
	if strings.ContainsAny(secret, "\r\n") {
		return "", fmt.Errorf("%s must be a single line", kind)
	}
	if secret == "" {
		return "", fmt.Errorf("%s from standard input is empty", kind)
	}
	return secret, nil
}
