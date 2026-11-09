package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/version"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestDevelopmentRuntimeIsExplicitAndIsolated(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	version.Version = "dev"
	t.Setenv("DOOMER_DEV_CLI", "1")
	api := "https://doomer-marc.ngrok.app/api"
	if !developmentRuntime(api, "local-dev") {
		t.Fatal("local build was not selected")
	}
	for _, endpoint := range []string{"https://doomer.ai/api", "http://doomer-marc.ngrok.app/api", "https://ngrok.app.evil.example/api"} {
		if developmentRuntime(endpoint, "local-dev") {
			t.Fatal(endpoint)
		}
	}
	if developmentRuntime(api, "default") {
		t.Fatal("default profile accepted")
	}
	version.Version = "2026.11.7.1"
	if developmentRuntime(api, "local-dev") {
		t.Fatal("released CLI accepted")
	}
	version.Version = "dev"
	t.Setenv("DOOMER_DEV_CLI", "")
	if developmentRuntime(api, "local-dev") {
		t.Fatal("implicit local runtime")
	}
}

func TestDevelopmentPrepareAndResumeUseCurrentBinary(t *testing.T) {
	isolate(t)
	t.Setenv("DOOMER_DEV_CLI", "1")
	repo := newWatchTestRepo(t)
	if out, err := execute(t, "", "install", "--repo", repo); err != nil {
		t.Fatal(out, err)
	}
	creations := 0
	anonymousApproved := false
	var originalRequest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/setup/grants":
			creations++
			var input adversarylabs.ManagedSetupRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				return
			}
			if !input.AnonymousAllowed {
				t.Error("anonymous capability missing")
			}
			if originalRequest != "" && input.Request != originalRequest {
				t.Error("request replaced")
			}
			originalRequest = input.Request
			fmt.Fprintf(w, `{"url":"http://%s/cli/setup#request=%s","expiresIn":1800}`, r.Host, input.Request)
		case "/api/v1/setup/grants/poll":
			if anonymousApproved {
				fmt.Fprint(w, `{"state":"approved","anonymous":true,"portable_state":false}`)
			} else {
				fmt.Fprint(w, `{"state":"pending"}`)
			}
		default:
			t.Errorf("unexpected network access: %s", r.URL)
			http.Error(w, "unexpected request", 500)
		}
	}))
	defer server.Close()
	api := server.URL + "/api"
	prefix := []string{"--api-url", api, "--profile", "local-dev"}
	run := func(args ...string) managedResult {
		t.Helper()
		out, err := execute(t, "", append(append([]string{}, prefix...), args...)...)
		if err != nil {
			t.Fatal(out, err)
		}
		var result managedResult
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(out, err)
		}
		return result
	}
	result := run("prepare", "--repo", repo, "--agent", "claude")
	if result.Release != "dev" || !result.SoftwareReady || result.State != "approval_required" {
		t.Fatal(result)
	}
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Load(result.Task)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := managedTaskBinary(task)
	executable, _ := os.Executable()
	if err != nil || binary != executable {
		t.Fatal(binary, err)
	}
	if !strings.Contains(strings.ToLower(os.Getenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE")), "desktop") && (len(result.PortableSetupCommand) < 3 || result.PortableSetupCommand[2] != executable) {
		t.Fatal(result.PortableSetupCommand)
	}

	file := filepath.Join(s.Root, "approvals", task.Handle+".json")
	var pending pendingManagedApproval
	if err := watch.Read(file, &pending); err != nil {
		t.Fatal(err)
	}
	originalVerifier := pending.Verifier
	pending.Input.AnonymousAllowed = false
	if err := watch.Write(file, pending); err != nil {
		t.Fatal(err)
	}
	task.Release = "2026.11.7.1"
	if err := s.Save(&task); err != nil {
		t.Fatal(err)
	}
	handled, err := dispatchManaged(context.Background(), []string{"prepare", "--task", task.Handle}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if handled || err != nil {
		t.Fatal("old runtime intercepted local build", handled, err)
	}
	result = run("prepare", "--task", task.Handle)
	if creations != 2 || result.Release != "dev" {
		t.Fatal("pending request was not upgraded", creations, result)
	}
	if err := watch.Read(file, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Verifier != originalVerifier || pending.Input.Request != originalRequest || time.Now().After(pending.Expires) {
		t.Fatal("request identity changed")
	}
	anonymousApproved = true
	result = run("prepare", "--task", task.Handle)
	if result.State != "ready" || !result.Anonymous || !result.UploadApproved {
		t.Fatal(result)
	}
	config := adversarylabs.ConfigStore{Path: filepath.Join(filepath.Dir(s.Root), "config.json")}
	auth, ok, err := config.StoredAuthE(adversarylabs.AuthKey(task.API, task.Profile))
	if err != nil || !ok || auth.Token != pending.Credential {
		t.Fatal("anonymous token was not retained", err)
	}
	t.Setenv("DOOMER_DEV_CLI", "")
	if _, err = managedTaskBinary(managed.Task{Release: "dev", API: task.API, Profile: task.Profile}); err == nil {
		t.Fatal("development task escaped opt-in")
	}
}

func TestDevelopmentHooksUseLocalBinary(t *testing.T) {
	isolate(t)
	t.Setenv("DOOMER_DEV_CLI", "1")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", home)
	out, err := execute(t, "", "--api-url", "https://doomer-marc.ngrok.app/api", "--profile", "local-dev", "hooks", "--agent", "codex")
	if err != nil {
		t.Fatal(out, err)
	}
	data, err := os.ReadFile(watch.HookFile(home, "codex"))
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	if !strings.Contains(string(data), "env DOOMER_DEV_CLI=1") || strings.Contains(string(data), executable) || !strings.Contains(string(data), "development") {
		t.Fatal(string(data))
	}
}

func TestAnonymousSetupKeepsSetupOnlyInstructions(t *testing.T) {
	instructions := managedTaskInstructions(managed.Task{AccountID: "anonymous:test", Purpose: "setup"})
	if !strings.Contains(instructions, "installation capability") || !strings.Contains(instructions, "Do not submit repository content or run a code review") {
		t.Fatal(instructions)
	}
}

func TestDevelopmentHookBinarySurvivesSourceRemoval(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "doomer")
	if err := os.WriteFile(source, []byte("development binary"), 0700); err != nil {
		t.Fatal(err)
	}
	staged, err := stageDevelopmentBinary(source, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(staged)
	if err != nil || string(contents) != "development binary" {
		t.Fatal(string(contents), err)
	}
	info, err := os.Stat(staged)
	if err != nil || info.Mode().Perm()&0100 == 0 {
		t.Fatal("staged binary is not executable", err)
	}
}
