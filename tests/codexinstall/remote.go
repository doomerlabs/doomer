package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const hostFingerprint = "SHA256:JJOP/lwiBGOMilfONPWZCXUrfK154cnJFXcqlsi6lPo"

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

type remote struct{ key, known string }

func (r remote) args(host string) []string {
	return []string{"-i", r.key, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=20", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + r.known, host}
}
func (r remote) ssh(ctx context.Context, host string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ssh", append(r.args(host), args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("SSH operation failed: %w", err)
	}
	return out, nil
}
func (r remote) trust(ctx context.Context, host string) error {
	raw, err := exec.CommandContext(ctx, "ssh-keyscan", "-T", "10", "-t", "rsa", host).Output()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-lf", "-")
	cmd.Stdin = bytes.NewReader(raw)
	fingerprints, err := cmd.Output()
	if err != nil {
		return err
	}
	if !strings.Contains(string(fingerprints), hostFingerprint) {
		return fmt.Errorf("unexpected exe.dev SSH host fingerprint")
	}
	f, err := os.OpenFile(r.known, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(raw)
	return err
}
func runRemote(ctx context.Context, c config, version string) (result report) {
	result.Model = c.Model
	fail := func(err error) report { result.Error = err.Error(); return result }
	key := os.Getenv("TEST_EXE_SSH_PRIVATE_KEY")
	if key == "" || c.APIKey == "" {
		return fail(fmt.Errorf("set TEST_EXE_SSH_PRIVATE_KEY and TEST_OPENAI_API_KEY"))
	}
	if version != "0.167.0" {
		return fail(fmt.Errorf("invalid pinned Codex version"))
	}
	dir, err := os.MkdirTemp("", "doomer-codex-e2e-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(dir)
	r := remote{filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")}
	if err = os.WriteFile(r.key, []byte(key+"\n"), 0600); err != nil {
		return fail(err)
	}
	if err = r.trust(ctx, "exe.dev"); err != nil {
		return fail(err)
	}
	raw, err := r.ssh(ctx, "exe.dev", nil, "new", "--json")
	if err != nil {
		return fail(err)
	}
	var vm struct {
		Name string `json:"vm_name"`
		Host string `json:"ssh_host"`
		Dest string `json:"ssh_dest"`
	}
	if err = json.Unmarshal(raw, &vm); err != nil {
		return fail(fmt.Errorf("decode created VM: %w", err))
	}
	if !safeName.MatchString(vm.Name) {
		return fail(fmt.Errorf("unexpected VM destination; inspect exe.dev for cleanup"))
	}
	result.VM = vm.Name
	fmt.Fprintln(os.Stderr, "Created test VM", vm.Name)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		_, err := r.ssh(cleanup, "exe.dev", nil, "rm", vm.Name)
		result.Deleted = err == nil
		if err != nil {
			result.Passed = false
			result.Error += "; cleanup failed for " + vm.Name
		}
	}()
	if !safeName.MatchString(vm.Host) || !strings.HasSuffix(vm.Host, ".exe.xyz") || !regexp.MustCompile(`^([A-Za-z0-9._+-]+@)?[A-Za-z0-9.-]+\.exe\.xyz$`).MatchString(vm.Dest) {
		return fail(fmt.Errorf("unexpected VM SSH destination"))
	}
	if err = r.trust(ctx, vm.Host); err != nil {
		return fail(err)
	}
	binary := filepath.Join(dir, "runner")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./tests/codexinstall")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return fail(fmt.Errorf("build worker: %w: %s", err, out))
	}
	// No secret is included in an SSH command line or copied file.
	install := `set -eu
mkdir -p /tmp/doomer-codex-e2e
cd /tmp/doomer-codex-e2e
curl -fsSL --retry 2 https://github.com/openai/codex/releases/download/rust-v0.167.0/codex-package-x86_64-unknown-linux-musl.tar.gz -o codex.tar.gz
printf '%s  codex.tar.gz\n' 3bbf75e83de6b58aed28f2cf6e33ad423ec31fd4700a1457b94be557ea5a7c6e | sha256sum -c -
tar -xzf codex.tar.gz
`
	if _, err = r.ssh(ctx, vm.Dest, nil, install); err != nil {
		return fail(fmt.Errorf("install pinned Codex: %w", err))
	}
	scpArgs := append(r.args("")[:len(r.args(""))-1], binary, vm.Dest+":/tmp/doomer-codex-e2e/runner")
	if out, err := exec.CommandContext(ctx, "scp", scpArgs...).CombinedOutput(); err != nil {
		return fail(fmt.Errorf("copy worker: %w: %s", err, out))
	}
	c.Codex = "/tmp/doomer-codex-e2e/bin/codex"
	payload, _ := json.Marshal(c)
	raw, err = r.ssh(ctx, vm.Dest, payload, "/tmp/doomer-codex-e2e/runner --worker")
	if err != nil {
		return fail(err)
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return fail(fmt.Errorf("decode worker report: %w", err))
	}
	result.VM = vm.Name
	return result
}
