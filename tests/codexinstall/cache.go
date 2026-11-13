package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxCacheObject = 32 << 20

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type cacheProvenance struct {
	Fingerprint    string    `json:"fingerprint"`
	Hit            bool      `json:"hit"`
	Result         string    `json:"result,omitempty"`
	Origin         string    `json:"origin_environment,omitempty"`
	ExecutedCommit string    `json:"executed_commit,omitempty"`
	ExecutedAt     time.Time `json:"executed_at,omitempty"`
}
type cacheArtifact struct {
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}
type cachedResult struct {
	Schema      int              `json:"schema"`
	Fingerprint string           `json:"fingerprint"`
	Scenario    string           `json:"scenario"`
	Harness     string           `json:"harness"`
	Outcome     string           `json:"outcome"`
	Inputs      snapshotMetadata `json:"inputs"`
	Started     time.Time        `json:"started"`
	Ended       time.Time        `json:"ended"`
	Duration    float64          `json:"duration_seconds"`
	Attempts    int              `json:"attempts"`
	Artifacts   []cacheArtifact  `json:"artifacts"`
	KeyID       string           `json:"key_id"`
}
type signedResult struct {
	KeyID     string `json:"key_id"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}
type cacheStore interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, string, []byte) error
}

type r2CacheStore struct{ bucket, endpoint, access, secret string }

func newR2CacheStore() (cacheStore, error) {
	account, bucket := os.Getenv("E2E_R2_ACCOUNT_ID"), os.Getenv("E2E_R2_BUCKET")
	access, secret := os.Getenv("E2E_R2_ACCESS_KEY_ID"), os.Getenv("E2E_R2_SECRET_ACCESS_KEY")
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(account) || bucket != "doomer-agent-e2e" || access == "" || secret == "" {
		return nil, fmt.Errorf("dedicated E2E R2 credentials are not configured")
	}
	if _, err := exec.LookPath("aws"); err != nil {
		return nil, fmt.Errorf("AWS CLI is required for E2E R2 storage")
	}
	return r2CacheStore{bucket, "https://" + account + ".r2.cloudflarestorage.com", access, secret}, nil
}
func (s r2CacheStore) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "aws", append([]string{"--endpoint-url", s.endpoint, "--region", "auto", "--no-cli-pager", "--cli-connect-timeout", "5", "--cli-read-timeout", "15", "s3api"}, args...)...)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "AWS_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "AWS_ACCESS_KEY_ID="+s.access, "AWS_SECRET_ACCESS_KEY="+s.secret, "AWS_EC2_METADATA_DISABLED=true", "AWS_MAX_ATTEMPTS=1", "AWS_CONFIG_FILE=/dev/null", "AWS_SHARED_CREDENTIALS_FILE=/dev/null")
	return cmd
}
func (s r2CacheStore) Get(ctx context.Context, key string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "e2e-cache-get-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "object")
	// Do not expose raw credential-bearing client diagnostics in reports/logs.
	if err = s.command(ctx, "get-object", "--bucket", s.bucket, "--key", key, "--range", "bytes=0-33554432", path).Run(); err != nil {
		return nil, fmt.Errorf("R2 object unavailable: %s", key)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCacheObject+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCacheObject {
		return nil, fmt.Errorf("cache object too large")
	}
	return data, nil
}
func (s r2CacheStore) Put(ctx context.Context, key string, data []byte) error {
	if len(data) > maxCacheObject {
		return fmt.Errorf("cache object too large")
	}
	dir, err := os.MkdirTemp("", "e2e-cache-put-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "object")
	if err = os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	if err = s.command(ctx, "put-object", "--bucket", s.bucket, "--key", key, "--body", path).Run(); err != nil {
		return fmt.Errorf("R2 upload failed: %s", key)
	}
	return nil
}

func cacheResultKey(m snapshotMetadata, scenario string) string {
	return "v1/results/" + m.fingerprint(scenario) + ".json"
}
func loadCacheKeys() (map[string]ed25519.PublicKey, error) {
	raw := []byte(os.Getenv("E2E_CACHE_PUBLIC_KEYS"))
	if len(raw) == 0 {
		var err error
		raw, err = os.ReadFile("tests/codexinstall/cache-keys.json")
		if err != nil {
			return nil, fmt.Errorf("E2E verification keys are not configured")
		}
	}
	var encoded map[string]string
	if json.Unmarshal(raw, &encoded) != nil {
		return nil, fmt.Errorf("invalid E2E verification keys")
	}
	keys := map[string]ed25519.PublicKey{}
	for id, value := range encoded {
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid E2E public key")
		}
		keys[id] = key
	}
	return keys, nil
}

func verifyCachedResult(raw []byte, m snapshotMetadata, scenario string, keys map[string]ed25519.PublicKey, now time.Time) (cachedResult, error) {
	var result cachedResult
	var envelope signedResult
	if json.Unmarshal(raw, &envelope) != nil {
		return result, fmt.Errorf("malformed signed result")
	}
	key, ok := keys[envelope.KeyID]
	if !ok {
		return result, fmt.Errorf("unknown signing key %q", envelope.KeyID)
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return result, fmt.Errorf("invalid signed payload")
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(key, payload, signature) {
		return result, fmt.Errorf("result signature mismatch")
	}
	if json.Unmarshal(payload, &result) != nil || result.Schema != fingerprintSchema || result.Inputs.Schema != fingerprintSchema || m.Schema != fingerprintSchema || result.KeyID != envelope.KeyID {
		return result, fmt.Errorf("invalid result schema")
	}
	if result.Fingerprint != m.fingerprint(scenario) || result.Inputs.fingerprint(scenario) != result.Fingerprint || result.Scenario != scenario || result.Harness != m.Settings["harness"] {
		return result, fmt.Errorf("result fingerprint mismatch")
	}
	if result.Outcome != "passed" {
		return result, fmt.Errorf("cached failure is not reusable")
	}
	if result.Started.IsZero() || result.Ended.Before(result.Started) || result.Ended.After(now.Add(5*time.Minute)) || result.Duration < 0 || result.Attempts != 1 {
		return result, fmt.Errorf("invalid execution timing")
	}
	if len(result.Artifacts) != 2 {
		return result, fmt.Errorf("result is missing artifacts")
	}
	seen := map[string]bool{}
	for _, artifact := range result.Artifacts {
		ext := ""
		switch artifact.Kind {
		case "report":
			ext = "json"
		case "replay":
			ext = "html"
		default:
			return result, fmt.Errorf("unknown artifact")
		}
		if seen[artifact.Kind] || !digestPattern.MatchString(artifact.SHA256) || artifact.Key != "v1/artifacts/"+artifact.SHA256+"."+ext {
			return result, fmt.Errorf("invalid artifact reference")
		}
		seen[artifact.Kind] = true
	}
	return result, nil
}

func reuseCachedResult(ctx context.Context, store cacheStore, m snapshotMetadata, scenario, output string, keys map[string]ed25519.PublicKey) error {
	raw, err := store.Get(ctx, cacheResultKey(m, scenario))
	if err != nil {
		return err
	}
	result, err := verifyCachedResult(raw, m, scenario, keys, time.Now())
	if err != nil {
		return err
	}
	artifacts := map[string][]byte{}
	for _, artifact := range result.Artifacts {
		raw, err := store.Get(ctx, artifact.Key)
		if err != nil {
			return err
		}
		if byteHash(raw) != artifact.SHA256 {
			return fmt.Errorf("artifact hash mismatch")
		}
		artifacts[artifact.Kind] = raw
	}
	var r report
	if json.Unmarshal(artifacts["report"], &r) != nil || !cacheReportPassed(r, m) || (r.Execution == "local" && !r.LocalCleaned) || (r.Execution == "remote" && !r.Deleted) || r.Scenario != scenario || r.Harness != m.Settings["harness"] || r.AuthMode != m.Settings["auth"] {
		return fmt.Errorf("invalid cached report")
	}
	r.Cache = &cacheProvenance{Fingerprint: m.fingerprint(scenario), Hit: true, Result: cacheResultKey(m, scenario), Origin: m.Environment, ExecutedCommit: result.Inputs.Commit, ExecutedAt: result.Ended}
	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(output+".html", artifacts["replay"], 0600); err != nil {
		return err
	}
	return os.WriteFile(output, append(encoded, '\n'), 0600)
}

func publishCachedResult(ctx context.Context, store cacheStore, m snapshotMetadata, scenario, output string, started time.Time) error {
	private, err := base64.StdEncoding.DecodeString(os.Getenv("E2E_CACHE_SIGNING_KEY"))
	if err != nil || len(private) != ed25519.PrivateKeySize {
		return fmt.Errorf("dedicated E2E signing key is unavailable")
	}
	keyID := os.Getenv("E2E_CACHE_SIGNING_KEY_ID")
	keys, err := loadCacheKeys()
	if err != nil {
		return err
	}
	key, ok := keys[keyID]
	if !ok || !ed25519.PublicKey(key).Equal(ed25519.PrivateKey(private).Public()) {
		return fmt.Errorf("E2E signing key is not in the verification keyring")
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		return err
	}
	var r report
	if json.Unmarshal(raw, &r) != nil {
		return fmt.Errorf("invalid report")
	}
	result := cachedResult{Schema: fingerprintSchema, Fingerprint: m.fingerprint(scenario), Scenario: scenario, Harness: m.Settings["harness"], Outcome: "failed", Inputs: m, Started: started, Ended: time.Now().UTC(), Duration: time.Since(started).Seconds(), Attempts: 1, KeyID: keyID}
	if cacheReportPassed(r, m) {
		result.Outcome = "passed"
	}
	for _, artifact := range []struct{ kind, path, ext string }{{"report", output, "json"}, {"replay", output + ".html", "html"}} {
		data, err := os.ReadFile(artifact.path)
		if err != nil {
			return err
		}
		hash := byteHash(data)
		ref := cacheArtifact{artifact.kind, "v1/artifacts/" + hash + "." + artifact.ext, hash}
		if err = store.Put(ctx, ref.Key, data); err != nil {
			return err
		}
		result.Artifacts = append(result.Artifacts, ref)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	envelope := signedResult{keyID, base64.StdEncoding.EncodeToString(payload), base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload))}
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	path := cacheResultKey(m, scenario)
	if !cacheReportPassed(r, m) {
		path = "v1/failures/" + result.Fingerprint + "/" + fmt.Sprint(result.Ended.UnixNano()) + ".json"
	}
	// Artifacts are durable before the discoverable result is published.
	return store.Put(ctx, path, data)
}

type scenarioCache struct {
	metadata snapshotMetadata
	store    cacheStore
	keys     map[string]ed25519.PublicKey
	started  time.Time
}

func openScenarioCache(ctx context.Context, m *snapshotMetadata, noCache bool, scenario, output string) (*scenarioCache, bool) {
	if scenario == "global-npx-discovery" {
		fmt.Fprintln(os.Stderr, "Cache bypass: global npx discovery exercises the live published skill")
		return nil, false
	}
	if m == nil {
		return nil, false
	}
	if noCache {
		fmt.Fprintln(os.Stderr, "Cache bypass: --no-cache")
		return nil, false
	}
	if m.CacheDisabled != "" {
		fmt.Fprintln(os.Stderr, "Cache miss:", m.CacheDisabled)
		return nil, false
	}
	store, err := newR2CacheStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cache miss:", err)
		return nil, false
	}
	keys, err := loadCacheKeys()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cache miss:", err)
		return nil, false
	}
	lookup, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, candidate := range cacheLookupEnvironments(*m) {
		if err = reuseCachedResult(lookup, store, candidate, scenario, output, keys); err == nil {
			fmt.Fprintln(os.Stderr, "Cache hit:", candidate.fingerprint(scenario), "from", candidate.Environment, "(no agent calls)")
			return nil, true
		}
	}
	fmt.Fprintln(os.Stderr, "Cache miss:", err)
	return &scenarioCache{*m, store, keys, time.Now().UTC()}, false
}
func (c *scenarioCache) publish(scenario, output string) {
	if c == nil {
		return
	}
	data, err := os.ReadFile(output)
	var result report
	if err != nil || json.Unmarshal(data, &result) != nil {
		fmt.Fprintln(os.Stderr, "Cache publication skipped: invalid scenario report")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := unchangedExternalInputs(ctx, c.metadata); err != nil {
		fmt.Fprintln(os.Stderr, "Cache publication skipped:", err)
		return
	}
	if err := publishCachedResult(ctx, c.store, c.metadata, scenario, output, c.started); err != nil {
		fmt.Fprintln(os.Stderr, "Cache publication skipped:", err)
		return
	}
	if result.Passed {
		fmt.Fprintln(os.Stderr, "Published signed E2E success:", c.metadata.fingerprint(scenario))
	} else {
		fmt.Fprintln(os.Stderr, "Published diagnostic E2E failure (not reusable):", c.metadata.fingerprint(scenario))
	}
}

func cacheReportPassed(r report, m snapshotMetadata) bool { return r.Passed }

// CI accepts locally executed Linux results only on the same architecture and
// with every content, agent, toolchain, auth and execution input unchanged.
// OS remains part of identity: macOS results never satisfy Linux checks.
func cacheLookupEnvironments(m snapshotMetadata) []snapshotMetadata {
	candidates := []snapshotMetadata{m}
	if m.OS == "linux" && (m.Environment == "local" || m.Environment == "ci") {
		alternate := m
		if m.Environment == "local" {
			alternate.Environment = "ci"
		} else {
			alternate.Environment = "local"
		}
		candidates = append(candidates, alternate)
	}
	return candidates
}
