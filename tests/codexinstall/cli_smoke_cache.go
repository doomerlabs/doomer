package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type cliSmokeCache struct {
	local  string
	remote cacheStore
	keys   map[string]ed25519.PublicKey
}

func openCLISmokeCache() *cliSmokeCache {
	keys, err := loadCacheKeys()
	if err != nil {
		fmt.Fprintln(os.Stderr, "CLI smoke cache unavailable:", err)
		return nil
	}
	local := os.Getenv("E2E_CLI_SMOKE_CACHE_DIR")
	if local == "" {
		if home, err := os.UserCacheDir(); err == nil {
			local = filepath.Join(home, "doomer", "cli-smoke")
		}
	}
	remote, _ := newR2CacheStore()
	return &cliSmokeCache{local: local, remote: remote, keys: keys}
}

func cliSmokeResultKey(fingerprint string) string {
	return "v1/cli-smoke/" + fingerprint + ".json"
}

func verifyCLISmokeResult(raw []byte, expected cliSmokeResult, keys map[string]ed25519.PublicKey, now time.Time) (cliSmokeResult, error) {
	var result cliSmokeResult
	var envelope signedResult
	if len(raw) > 1<<20 || json.Unmarshal(raw, &envelope) != nil {
		return result, fmt.Errorf("invalid CLI smoke envelope")
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return result, fmt.Errorf("invalid CLI smoke payload")
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	key, known := keys[envelope.KeyID]
	if err != nil || !known || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, payload, signature) {
		return result, fmt.Errorf("CLI smoke signature mismatch")
	}
	if json.Unmarshal(payload, &result) != nil || result.Schema != 1 || result.Error != "" || result.CacheHit || result.Bounded != expected.Bounded || result.Binary != expected.Binary || result.Runner != expected.Runner || result.OS != expected.OS || result.Arch != expected.Arch || result.Fingerprint != expected.Fingerprint || result.Fingerprint != cliSmokeFingerprint(result.Binary, result.Runner) {
		return result, fmt.Errorf("CLI smoke result identity mismatch")
	}
	if result.Started.IsZero() || result.Ended.Before(result.Started) || result.Ended.After(now.Add(5*time.Minute)) || now.Sub(result.Started) > cacheMaxAge {
		return result, fmt.Errorf("CLI smoke result expired or invalid timing")
	}
	if !cliSmokePassed(result) {
		return result, fmt.Errorf("CLI smoke result is missing successful checks")
	}
	return result, nil
}

func cliSmokePassed(result cliSmokeResult) bool {
	if result.Error != "" || len(result.Checks) != 4 {
		return false
	}
	for _, check := range []string{"version", "login", "profile_selection", "logout_removes_token"} {
		if !result.Checks[check] {
			return false
		}
	}
	return true
}

func (c *cliSmokeCache) load(ctx context.Context, expected cliSmokeResult) (cliSmokeResult, error) {
	if !digestPattern.MatchString(expected.Fingerprint) {
		return cliSmokeResult{}, fmt.Errorf("invalid CLI smoke cache key")
	}
	if c.local != "" {
		if raw, err := os.ReadFile(filepath.Join(c.local, expected.Fingerprint+".json")); err == nil {
			if result, err := verifyCLISmokeResult(raw, expected, c.keys, time.Now()); err == nil {
				return result, nil
			}
		}
	}
	if c.remote != nil {
		raw, err := c.remote.Get(ctx, cliSmokeResultKey(expected.Fingerprint))
		if err != nil {
			return cliSmokeResult{}, err
		}
		result, err := verifyCLISmokeResult(raw, expected, c.keys, time.Now())
		if err == nil {
			_ = c.saveLocal(expected.Fingerprint, raw)
		}
		return result, err
	}
	return cliSmokeResult{}, fmt.Errorf("no signed CLI smoke result")
}

func (c *cliSmokeCache) saveLocal(fingerprint string, data []byte) error {
	if c.local == "" {
		return nil
	}
	if !digestPattern.MatchString(fingerprint) {
		return fmt.Errorf("invalid CLI smoke cache key")
	}
	if err := os.MkdirAll(c.local, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(c.local, ".result-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(c.local, fingerprint+".json"))
}

func (c *cliSmokeCache) publish(ctx context.Context, result cliSmokeResult) error {
	if !cliSmokePassed(result) || result.CacheHit {
		return fmt.Errorf("only newly executed successes may be published")
	}
	private, err := base64.StdEncoding.DecodeString(os.Getenv("E2E_CACHE_SIGNING_KEY"))
	keyID := os.Getenv("E2E_CACHE_SIGNING_KEY_ID")
	key, known := c.keys[keyID]
	if err != nil || len(private) != ed25519.PrivateKeySize || !known || !key.Equal(ed25519.PrivateKey(private).Public()) {
		return fmt.Errorf("dedicated E2E signing key unavailable")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(signedResult{keyID, base64.StdEncoding.EncodeToString(payload), base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload))})
	if err != nil {
		return err
	}
	if err := c.saveLocal(result.Fingerprint, raw); err != nil {
		return err
	}
	if c.remote != nil {
		if err := c.remote.Put(ctx, cliSmokeResultKey(result.Fingerprint), raw); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "Published signed CLI smoke result:", result.Fingerprint)
	return nil
}
