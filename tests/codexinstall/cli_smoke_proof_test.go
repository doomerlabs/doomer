package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Opt in because this proof builds three actual CLI binaries. It uses generated
// test-only keys and private temporary storage; no production credentials.
func TestCLISmokeReuseProof(t *testing.T) {
	if os.Getenv("E2E_CLI_SMOKE_PROOF") != "1" {
		t.Skip("set E2E_CLI_SMOKE_PROOF=1 to build and mutate real CLI binaries")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	if _, err := copySource(ctx, root, temp, "source", ""); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(temp, "source")
	build := func() (string, cliSmokeResult) {
		t.Helper()
		buildDir := t.TempDir()
		binary, bounded, err := buildCLISmoke(ctx, source, buildDir)
		if err != nil || !bounded {
			t.Fatalf("bounded build failed: %v, bounded=%v", err, bounded)
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		r := cliSmokeResult{Schema: 1, Binary: byteHash(data), Runner: byteHash([]byte("unchanged proof runner")), Bounded: bounded, OS: runtime.GOOS, Arch: runtime.GOARCH, Checks: map[string]bool{}}
		r.Fingerprint = cliSmokeFingerprint(r.Binary, r.Runner)
		return binary, r
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("E2E_CACHE_SIGNING_KEY", base64.StdEncoding.EncodeToString(private))
	t.Setenv("E2E_CACHE_SIGNING_KEY_ID", "proof")
	cache := &cliSmokeCache{local: t.TempDir(), keys: map[string]ed25519.PublicKey{"proof": public}}
	binary, baseline := build()
	baseline.Started = time.Now().UTC()
	if err := exerciseCLISmoke(ctx, binary, t.TempDir(), &baseline); err != nil {
		t.Fatal(err)
	}
	baseline.Ended = time.Now().UTC()
	if err := cache.publish(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	// An unplanned command must fail, even if called indirectly by future code.
	guard := exec.CommandContext(ctx, binary, "whoami")
	guard.Dir = t.TempDir()
	guard.Env = []string{"HOME=" + guard.Dir, "DOOMER_CONFIG_DIR=" + filepath.Join(guard.Dir, "config"), "PATH=/usr/bin:/bin"}
	if output, err := guard.CombinedOutput(); err == nil || !strings.Contains(string(output), "command outside bounded CLI smoke contract") {
		t.Fatalf("removed command did not fail closed: %v\n%s", err, output)
	}
	mutate := func(path, from, to string) {
		t.Helper()
		file := filepath.Join(source, path)
		data, err := os.ReadFile(file)
		if err != nil || strings.Count(string(data), from) != 1 {
			t.Fatalf("mutation anchor missing: %s: %v", path, err)
		}
		if err := os.Chmod(file, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(strings.Replace(string(data), from, to, 1)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mutate("cmd/whoami.go", `Profile: %s\nService: %s\n`, `Changed profile: %s\nService: %s\n`)
	_, unrelated := build()
	if unrelated.Fingerprint != baseline.Fingerprint {
		t.Fatal("unrelated handler change altered the actual bounded binary")
	}
	if reused, err := cache.load(ctx, unrelated); err != nil || !reused.Started.Equal(baseline.Started) {
		t.Fatalf("unrelated edit did not reuse the original signed result: %v", err)
	}
	mutate("cmd/version.go", `case "text":`, `case "deliberately-broken":`)
	binary, changed := build()
	if changed.Fingerprint == baseline.Fingerprint {
		t.Fatal("selected handler change kept the binary identity")
	}
	if _, err := cache.load(ctx, changed); err == nil {
		t.Fatal("reused a stale result after selected handler changed")
	}
	if err := exerciseCLISmoke(ctx, binary, t.TempDir(), &changed); err == nil {
		t.Fatal("relevant broken command did not fail when rerun")
	}
	mutate("cmd/version.go", `case "deliberately-broken":`, `case "text":`)
	mutate("cmd/root.go", `Short: "Connect to the Doomer SaaS"`, `Short: "Changed shared command metadata"`)
	_, shared := build()
	if shared.Fingerprint == baseline.Fingerprint {
		t.Fatal("shared command metadata was excluded from the binary identity")
	}
	if _, err := cache.load(ctx, shared); err == nil {
		t.Fatal("shared command metadata change reused the old result")
	}
	t.Log("real CLI proof: baseline passed; whoami edit reused; broken version edit missed and failed; shared metadata edit missed")
}
