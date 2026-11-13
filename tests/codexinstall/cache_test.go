package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type memoryCache struct {
	objects   map[string][]byte
	puts      []string
	failAfter int
}

func (s *memoryCache) Get(_ context.Context, key string) ([]byte, error) {
	raw, ok := s.objects[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return append([]byte(nil), raw...), nil
}
func (s *memoryCache) Put(_ context.Context, key string, raw []byte) error {
	if s.failAfter > 0 && len(s.puts) >= s.failAfter {
		return errors.New("storage outage")
	}
	s.objects[key] = append([]byte(nil), raw...)
	s.puts = append(s.puts, key)
	return nil
}
func testCacheMetadata() snapshotMetadata {
	return snapshotMetadata{Schema: fingerprintSchema, Files: []snapshotEntry{{Path: "cli/main.go", SHA256: byteHash([]byte("source")), Mode: 0600}}, Settings: map[string]string{"harness": "codex", "auth": "api", "model": "gpt-6.1-terra"}, Environment: "local", OS: "darwin", Arch: "arm64", Baseline: "2026.11.9.3"}
}
func testCacheKey(t *testing.T) map[string]ed25519.PublicKey {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{"test": base64.StdEncoding.EncodeToString(pub)}
	raw, _ := json.Marshal(keys)
	t.Setenv("E2E_CACHE_PUBLIC_KEYS", string(raw))
	t.Setenv("E2E_CACHE_SIGNING_KEY_ID", "test")
	t.Setenv("E2E_CACHE_SIGNING_KEY", base64.StdEncoding.EncodeToString(private))
	return map[string]ed25519.PublicKey{"test": pub}
}
func writeCacheReport(t *testing.T, path string, passed bool) {
	t.Helper()
	raw, _ := json.Marshal(report{Scenario: "install", Harness: "codex", AuthMode: "api", Passed: passed, Usage: usage{Reserved: .12}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".html", []byte("<html>private replay</html>"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSignedCacheRoundTripAndArtifactOrder(t *testing.T) {
	keys := testCacheKey(t)
	m := testCacheMetadata()
	store := &memoryCache{objects: map[string][]byte{}}
	path := filepath.Join(t.TempDir(), "report.json")
	writeCacheReport(t, path, true)
	if err := publishCachedResult(context.Background(), store, m, "install", path, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 3 || store.puts[2] != cacheResultKey(m, "install") {
		t.Fatalf("result published before artifacts: %v", store.puts)
	}
	result, err := verifyCachedResult(store.objects[store.puts[2]], m, "install", keys, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 1 || result.Inputs.Environment != "local" {
		t.Fatal(result)
	}
	target := filepath.Join(t.TempDir(), "hit.json")
	if err = reuseCachedResult(context.Background(), store, m, "install", target, keys); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(target)
	var r report
	json.Unmarshal(raw, &r)
	if !r.Passed || r.Cache == nil || !r.Cache.Hit || r.Usage.Reserved != .12 {
		t.Fatalf("lost cached provenance/accounting: %+v", r)
	}
	// A hit satisfies the same scenario without invoking any runner/provider.
	charged := runSuiteCommand(context.Background(), "/usr/bin/true", nil, "install", target, filepath.Join(t.TempDir(), "log"), .2, "api")
	if charged.Charged == nil || *charged.Charged != 0 || !charged.CacheHit {
		t.Fatalf("charged reused calls: %+v", charged)
	}
}

func TestCacheRejectsTamperingMissingArtifactsAndWrongInputs(t *testing.T) {
	keys := testCacheKey(t)
	m := testCacheMetadata()
	store := &memoryCache{objects: map[string][]byte{}}
	path := filepath.Join(t.TempDir(), "report.json")
	writeCacheReport(t, path, true)
	if err := publishCachedResult(context.Background(), store, m, "install", path, time.Now()); err != nil {
		t.Fatal(err)
	}
	raw := store.objects[cacheResultKey(m, "install")]
	var envelope signedResult
	json.Unmarshal(raw, &envelope)
	envelope.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64))
	tampered, _ := json.Marshal(envelope)
	if _, err := verifyCachedResult(tampered, m, "install", keys, time.Now()); err == nil {
		t.Fatal("accepted forged signature")
	}
	if _, err := verifyCachedResult(raw, m, "install", map[string]ed25519.PublicKey{}, time.Now()); err == nil {
		t.Fatal("accepted unknown key")
	}
	oldSchema := m
	oldSchema.Schema = 1
	if _, err := verifyCachedResult(raw, oldSchema, "install", keys, time.Now()); err == nil {
		t.Fatal("accepted old dependency schema")
	}
	changed := m
	changed.Baseline = "2026.11.10.1"
	if _, err := verifyCachedResult(raw, changed, "install", keys, time.Now()); err == nil {
		t.Fatal("accepted wrong baseline")
	}
	if _, err := verifyCachedResult(raw, m, "install", keys, time.Now().Add(-10*time.Minute)); err == nil {
		t.Fatal("accepted future execution timing")
	}
	artifact := store.puts[0]
	original := store.objects[artifact]
	store.objects[artifact] = []byte("changed")
	if err := reuseCachedResult(context.Background(), store, m, "install", filepath.Join(t.TempDir(), "hit"), keys); err == nil {
		t.Fatal("accepted corrupt artifact")
	}
	store.objects[artifact] = original
	delete(store.objects, store.puts[1])
	if err := reuseCachedResult(context.Background(), store, m, "install", filepath.Join(t.TempDir(), "hit"), keys); err == nil {
		t.Fatal("accepted incomplete upload")
	}
}

func TestCacheAcceptsOlderMatchingSuccess(t *testing.T) {
	keys := testCacheKey(t)
	m := testCacheMetadata()
	store := &memoryCache{objects: map[string][]byte{}}
	path := filepath.Join(t.TempDir(), "report.json")
	writeCacheReport(t, path, true)
	if err := publishCachedResult(context.Background(), store, m, "install", path, time.Now()); err != nil {
		t.Fatal(err)
	}
	raw := store.objects[cacheResultKey(m, "install")]
	for _, age := range []time.Duration{7 * time.Hour, 8 * 24 * time.Hour} {
		if _, err := verifyCachedResult(raw, m, "install", keys, time.Now().Add(age)); err != nil {
			t.Fatalf("rejected matching success after %s: %v", age, err)
		}
	}
}

func TestFailedAndInterruptedPublicationsDoNotCreateSuccess(t *testing.T) {
	testCacheKey(t)
	m := testCacheMetadata()
	path := filepath.Join(t.TempDir(), "report.json")
	writeCacheReport(t, path, false)
	store := &memoryCache{objects: map[string][]byte{}}
	if err := publishCachedResult(context.Background(), store, m, "install", path, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.objects[cacheResultKey(m, "install")]; ok {
		t.Fatal("published failure as reusable success")
	}
	writeCacheReport(t, path, true)
	store = &memoryCache{objects: map[string][]byte{}, failAfter: 1}
	if err := publishCachedResult(context.Background(), store, m, "install", path, time.Now()); err == nil {
		t.Fatal("ignored outage")
	}
	if _, ok := store.objects[cacheResultKey(m, "install")]; ok {
		t.Fatal("published incomplete result")
	}
}

func TestFingerprintIgnoresCommitButIncludesAllBehaviorInputs(t *testing.T) {
	original := testCacheMetadata()
	base := original.fingerprint("install")
	original.Commit = "rebase"
	original.Captured = time.Now()
	if original.fingerprint("install") != base {
		t.Fatal("commit invalidated content identity")
	}
	for _, change := range []func(*snapshotMetadata){
		func(m *snapshotMetadata) { m.Files = append(m.Files, snapshotEntry{Path: "new.go", SHA256: "new"}) },
		func(m *snapshotMetadata) { m.Files = nil }, func(m *snapshotMetadata) { m.Files[0].SHA256 = "modified" },
		func(m *snapshotMetadata) { m.Settings["model"] = "other" }, func(m *snapshotMetadata) { m.Environment = "ci" },
		func(m *snapshotMetadata) { m.OS = "linux" }, func(m *snapshotMetadata) { m.Arch = "amd64" },
		func(m *snapshotMetadata) { m.Baseline = "different" }, func(m *snapshotMetadata) { m.External = []externalInput{{URL: "url", SHA256: "different"}} },
	} {
		m := testCacheMetadata()
		change(&m)
		if m.fingerprint("install") == base {
			t.Fatal("behavior input did not invalidate")
		}
	}
	if original.fingerprint("upgrade") == base {
		t.Fatal("scenario did not invalidate")
	}
}
func TestSnapshotIncludesNewAndDeletedFilesAndFreezesBytes(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	ctx := context.Background()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	source := filepath.Join(root, "main.go")
	os.WriteFile(source, []byte("original"), 0600)
	exec.Command("git", "-C", root, "add", "main.go").Run()
	os.WriteFile(filepath.Join(root, "new.go"), []byte("new"), 0600)
	os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0600)
	entries, err := copySource(ctx, root, dest, "cli", filepath.Join(root, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("wrong source membership: %+v", entries)
	}
	os.WriteFile(source, []byte("later edit"), 0600)
	frozen, _ := os.ReadFile(filepath.Join(dest, "cli", "main.go"))
	if string(frozen) != "original" {
		t.Fatal("working tree edit changed snapshot")
	}
	os.Remove(source)
	after, err := sourceInventory(ctx, root, "cli", "")
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(entries, after) {
		t.Fatal("deletion did not change manifest")
	}
}
func TestLiveR2RoundTrip(t *testing.T) {
	if os.Getenv("E2E_CACHE_LIVE_TEST") != "1" {
		t.Skip("set E2E_CACHE_LIVE_TEST=1 with dedicated Doppler test credentials")
	}
	store, err := newR2CacheStore()
	if err != nil {
		t.Fatal(err)
	}
	writer := store.(r2CacheStore)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	key := "v1/diagnostics/" + time.Now().UTC().Format("20060102T150405.000000000") + ".txt"
	defer writer.command(context.Background(), "delete-object", "--bucket", writer.bucket, "--key", key).Run()
	data := []byte("private E2E storage verification")
	if err := writer.Put(ctx, key, data); err != nil {
		t.Fatal(err)
	}
	found, err := writer.Get(ctx, key)
	if err != nil || string(found) != string(data) {
		t.Fatalf("writer round trip failed: %v", err)
	}
	reader := writer
	reader.access = os.Getenv("E2E_READ_R2_ACCESS_KEY_ID")
	reader.secret = os.Getenv("E2E_READ_R2_SECRET_ACCESS_KEY")
	if reader.access == "" || reader.secret == "" {
		t.Fatal("missing dedicated reader credentials")
	}
	if found, err = reader.Get(ctx, key); err != nil || string(found) != string(data) {
		t.Fatalf("reader could not verify object: %v", err)
	}
	if err = reader.Put(ctx, key, []byte("must not write")); err == nil {
		t.Fatal("reader unexpectedly has write access")
	}
}

func TestSnapshotCopiesAgentPackageResources(t *testing.T) {
	root, destination := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(root, "bin"), 0700)
	binary := filepath.Join(root, "bin", "codex")
	os.WriteFile(binary, []byte("executable"), 0700)
	resource := filepath.Join(root, "resource.json")
	os.WriteFile(resource, []byte("resource"), 0600)
	captured, entries, err := captureAgentPackage(context.Background(), binary, destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("missing package resources: %+v", entries)
	}
	os.WriteFile(binary, []byte("changed executable"), 0700)
	os.WriteFile(resource, []byte("changed resource"), 0600)
	data, err := os.ReadFile(captured)
	if err != nil || string(data) != "executable" {
		t.Fatal("agent executable was not frozen")
	}
	data, err = os.ReadFile(filepath.Join(destination, "agent", "resource.json"))
	if err != nil || string(data) != "resource" {
		t.Fatal("agent resource was not frozen")
	}
}

func TestSnapshotKeepsEmbeddedTemplateBuildFiles(t *testing.T) {
	if excludedSnapshotPath("templates/typescript/dist/index.js") {
		t.Fatal("excluded embedded template artifact")
	}
	if !excludedSnapshotPath("dist/doomer") {
		t.Fatal("included root build artifact")
	}
}

func TestSnapshotCapturesNestedCandidateSeparately(t *testing.T) {
	ctx := context.Background()
	root, destination := t.TempDir(), t.TempDir()
	candidate := filepath.Join(root, "baseline")
	git := func(dir string, args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git(root, "init", "-q")
	if err := os.Mkdir(candidate, 0700); err != nil {
		t.Fatal(err)
	}
	git(candidate, "init", "-q")
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "main.go"), "current CLI")
	// A lexical prefix match must not accidentally exclude an adjacent file.
	write(filepath.Join(root, "baseline-not-a-checkout.go"), "parent source")
	write(filepath.Join(candidate, "main.go"), "released CLI")
	git(root, "add", "main.go", "baseline-not-a-checkout.go")
	git(candidate, "add", "main.go")

	// This is the inventory shape produced by actions/checkout with path: baseline.
	inventory, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil || !strings.Contains(string(inventory), "baseline/\x00") {
		t.Fatalf("missing nested checkout fixture: %q %v", inventory, err)
	}
	sources := map[string]string{"cli": root, "candidate": candidate, "bootstrap": root}
	parentFiles, err := copySource(ctx, root, destination, "cli", "", nestedSnapshotSources(root, sources)...)
	if err != nil {
		t.Fatal(err)
	}
	candidateFiles, err := copySource(ctx, candidate, destination, "candidate", "", nestedSnapshotSources(candidate, sources)...)
	if err != nil {
		t.Fatal(err)
	}
	if len(parentFiles) != 2 || len(candidateFiles) != 1 {
		t.Fatalf("wrong role membership: parent=%+v candidate=%+v", parentFiles, candidateFiles)
	}
	for _, file := range parentFiles {
		if strings.HasPrefix(file.Path, "cli/baseline/") {
			t.Fatalf("nested candidate leaked into parent: %s", file.Path)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "cli", "baseline")); !os.IsNotExist(err) {
		t.Fatal("candidate directory copied into parent")
	}
	frozen := filepath.Join(destination, "candidate", "main.go")
	raw, err := os.ReadFile(frozen)
	if err != nil || string(raw) != "released CLI" {
		t.Fatalf("candidate was not captured: %q %v", raw, err)
	}
	before := testCacheMetadata()
	before.Files = append(parentFiles, candidateFiles...)
	write(filepath.Join(candidate, "main.go"), "changed released CLI")
	changedCandidate, err := sourceInventory(ctx, candidate, "candidate", "")
	if err != nil {
		t.Fatal(err)
	}
	after := before
	after.Files = append(append([]snapshotEntry(nil), parentFiles...), changedCandidate...)
	if before.fingerprint("policy-recovery") == after.fingerprint("policy-recovery") {
		t.Fatal("candidate change did not invalidate fingerprint")
	}
	raw, err = os.ReadFile(frozen)
	if err != nil || string(raw) != "released CLI" {
		t.Fatal("candidate edit changed its captured snapshot")
	}
	// Do not silently skip arbitrary unselected repositories or unsupported inputs.
	if _, err := sourceInventory(ctx, root, "cli", ""); err == nil {
		t.Fatal("unselected nested repository was silently ignored")
	}
}

func TestCICacheAcceptanceKeepsPlatformAndInputs(t *testing.T) {
	m := testCacheMetadata()
	m.OS = "linux"
	m.Environment = "ci"
	candidates := cacheLookupEnvironments(m)
	if len(candidates) != 2 || candidates[0].Environment != "ci" || candidates[1].Environment != "local" {
		t.Fatalf("unexpected acceptance: %+v", candidates)
	}
	local := m
	local.Environment = "local"
	if candidates[1].fingerprint("install") != local.fingerprint("install") {
		t.Fatal("matching Linux local result not considered")
	}
	local.OS = "darwin"
	if candidates[1].fingerprint("install") == local.fingerprint("install") {
		t.Fatal("macOS result satisfied Linux")
	}
	m.OS = "darwin"
	if len(cacheLookupEnvironments(m)) != 1 {
		t.Fatal("enabled cross-environment reuse outside Linux")
	}
}
