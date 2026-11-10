package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCLISmokeSignedReuse(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("E2E_CACHE_SIGNING_KEY", base64.StdEncoding.EncodeToString(private))
	t.Setenv("E2E_CACHE_SIGNING_KEY_ID", "test")
	cache := &cliSmokeCache{local: t.TempDir(), keys: map[string]ed25519.PublicKey{"test": public}}
	now := time.Now().UTC()
	result := cliSmokeResult{Schema: 1, Binary: strings.Repeat("a", 64), Runner: strings.Repeat("b", 64), OS: runtime.GOOS, Arch: runtime.GOARCH, Bounded: true, Started: now.Add(-time.Second), Ended: now, Checks: map[string]bool{"version": true, "login": true, "profile_selection": true, "logout_removes_token": true}}
	result.Fingerprint = cliSmokeFingerprint(result.Binary, result.Runner)
	if err := cache.publish(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cache.local, result.Fingerprint+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.load(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*cliSmokeResult)
	}{
		{"changed binary", func(r *cliSmokeResult) { r.Binary = strings.Repeat("c", 64) }},
		{"changed runner", func(r *cliSmokeResult) { r.Runner = strings.Repeat("c", 64) }},
		{"changed platform", func(r *cliSmokeResult) { r.Arch = "different" }},
		{"changed boundary", func(r *cliSmokeResult) { r.Bounded = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := result
			tc.edit(&expected)
			if _, err := verifyCLISmokeResult(raw, expected, cache.keys, now); err == nil {
				t.Fatal("reused a mismatched result")
			}
		})
	}
	if _, err := verifyCLISmokeResult(raw, result, cache.keys, now.Add(7*time.Hour)); err == nil {
		t.Fatal("reused an expired result")
	}
	var envelope signedResult
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	tampered, _ := json.Marshal(envelope)
	if _, err := verifyCLISmokeResult(tampered, result, cache.keys, now); err == nil {
		t.Fatal("reused a forged result")
	}
	result.Checks["logout_removes_token"] = false
	if err := cache.publish(context.Background(), result); err == nil {
		t.Fatal("published a failed smoke check")
	}
}
