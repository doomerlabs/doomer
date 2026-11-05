package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/publock"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

func newWatchTestRepo(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	reviewGit(t, path, "init")
	if err := os.WriteFile(filepath.Join(path, "sample.txt"), []byte("sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewGit(t, path, "add", ".")
	reviewGit(t, path, "commit", "-m", "initial")
	canonical, err := watching.Root(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

// A hosted grant carries the loopback callback opaquely in this test server.
func fakeSetupAPI(t *testing.T) string {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request setupGrantRequest
		if r.URL.Path != "/api/v1/setup/grants" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": server.URL + "/cli/setup#" + url.Values{"callback": {request.Callback}}.Encode()})
	}))
	t.Cleanup(server.Close)
	return server.URL + "/api"
}

func fakeSetupBrowser(t *testing.T, decision string, inspect func(string)) {
	t.Helper()
	previous := openSetupBrowser
	t.Cleanup(func() { openSetupBrowser = previous })
	openSetupBrowser = func(_ context.Context, link string) error {
		if inspect != nil {
			inspect(link)
		}
		u, err := url.Parse(link)
		if err != nil {
			return err
		}
		if u.Path == "/cli/setup" {
			fragment, _ := url.ParseQuery(u.Fragment)
			u, err = url.Parse(fragment.Get("callback"))
			if err != nil {
				return err
			}
		}
		u.Path = "/complete"
		q := u.Query()
		q.Set("decision", decision)
		u.RawQuery = q.Encode()
		response, err := http.Get(u.String())
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("callback returned %d", response.StatusCode)
		}
		return nil
	}
}

func TestAnonymousBrowserConsentIsExplicitAndRepoScoped(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	api, profile := fakeSetupAPI(t), "dev"
	repo, other := newWatchTestRepo(t), newWatchTestRepo(t)
	calls := 0
	fakeSetupBrowser(t, "allow", func(_ string) { calls++ })
	c := newWatchCommand(app, &api, &profile)
	c.SetContext(context.Background())
	c.SetIn(strings.NewReader(""))
	c.SetOut(&bytes.Buffer{})
	if err := c.Flags().Set("browser", "true"); err != nil {
		t.Fatal(err)
	}
	cfg, auth, err := prepareWatch(c, app, &api, &profile, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !cfg.ExplicitConsent || cfg.AllRepositories || !watching.ConsentValid(cfg, api, profile, auth.Token) || !watching.Allowed(cfg, repo) || watching.Allowed(cfg, other) {
		t.Fatalf("incorrect consent: %+v calls=%d", cfg, calls)
	}
	before := cfg.ConsentedAt
	if _, _, err := prepareWatch(c, app, &api, &profile, []string{repo}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("approved repository prompted again")
	}
	fakeSetupBrowser(t, "cancel", func(_ string) { calls++ })
	if _, _, err := prepareWatch(c, app, &api, &profile, []string{other}); err == nil {
		t.Fatal("cancelled consent succeeded")
	}
	_, file, _ := watchPaths(api, profile)
	if err := watching.Read(file, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.ConsentedAt.Equal(before) || watching.Allowed(cfg, other) {
		t.Fatal("cancellation modified approval")
	}
	fakeSetupBrowser(t, "allow", nil)
	cfg, _, err = prepareWatch(c, app, &api, &profile, []string{other})
	if err != nil || !watching.Allowed(cfg, repo) || !watching.Allowed(cfg, other) || cfg.AllRepositories {
		t.Fatalf("second repo approval: %+v %v", cfg, err)
	}
}

func TestLegacyAnonymousConsentMustBeRenewed(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	api, profile := "https://example.test/api", "dev"
	auth, err := watchAuth(app, api, profile)
	if err != nil {
		t.Fatal(err)
	}
	_, file, _ := watchPaths(api, profile)
	cfg := watching.Config{Version: 1, API: api, Profile: profile, AccountKey: watching.Key(auth.Token), AllRepositories: true, ConsentedAt: time.Now()}
	if err := watching.Write(file, cfg); err != nil {
		t.Fatal(err)
	}
	c := newWatchCommand(app, &api, &profile)
	c.SetContext(context.Background())
	c.SetIn(strings.NewReader("yes\n"))
	_ = c.Flags().Set("no-browser", "true")
	if _, _, err := prepareWatch(c, app, &api, &profile, []string{newWatchTestRepo(t)}); err == nil {
		t.Fatal("legacy anonymous approval bypassed consent")
	}
}

func TestWatchStatusDoesNotCreateConsentOrIdentity(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	repo := newWatchTestRepo(t)
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	api, profile := "http://localhost:3000/api", "dev"
	status, err := inspectWatch(app, api, profile, repo)
	if err != nil || status.Approved || status.Running || status.Paused {
		t.Fatalf("%+v %v", status, err)
	}
	root, _, _ := watchPaths(api, profile)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("status created private state")
	}
}

func TestWatchBackgroundLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("background watcher supports macOS and Linux")
	}
	// Build the actual CLI before isolating HOME so this exercises exec, detached
	// stdio, config propagation and process cleanup rather than a mocked launcher.
	binary := filepath.Join(t.TempDir(), "doomer")
	build := exec.Command("go", "build", "-o", binary, "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	repo, other := newWatchTestRepo(t), newWatchTestRepo(t)
	var requests atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Setup may report consent, but must never submit a review or another
		// lifecycle milestone before an agent edits a file.
		if r.URL.Path == "/api/v2/installations/activate" && r.Method == http.MethodPost {
			var event struct {
				Stage string `json:"stage"`
			}
			if err := json.NewDecoder(r.Body).Decode(&event); err == nil && event.Stage == "approved" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer service.Close()
	api, profile := service.URL+"/api", "dev"
	run := func(args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"--api-url", api, "--profile", profile, "watch"}, args...)...)
		command.Dir = repo
		out, err := command.CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() {
		if out, err := run("stop"); err != nil {
			t.Errorf("cleanup: %s %v", out, err)
		}
	})
	if out, err := run("start", "--yes"); err != nil {
		t.Fatalf("start: %s %v", out, err)
	}
	root, file, _ := watchPaths(api, profile)
	first, live, err := runningWatch(root)
	if err != nil || !live || first.ManagedSession == "" {
		t.Fatalf("not started: %+v %v", first, err)
	}
	if out, err := run("start"); err != nil || !strings.Contains(out, "Reusing") {
		t.Fatalf("reuse: %s %v", out, err)
	}
	second, _, _ := runningWatch(root)
	if second.PID != first.PID {
		t.Fatal("duplicate watcher started")
	}
	var status watchStatus
	if out, err := run("status", "--json"); err != nil {
		t.Fatalf("status: %s %v", out, err)
	} else if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Approved || !status.Running || status.API != api || !status.HooksInstalled["codex"] {
		t.Fatalf("status: %+v", status)
	}
	if out, err := run("start", other, "--no-browser"); err == nil {
		t.Fatalf("other repo bypassed approval: %s", out)
	}
	if out, err := run("start", other, "--yes"); err != nil {
		t.Fatalf("add repo: %s %v", out, err)
	}
	third, _, _ := runningWatch(root)
	if third.PID == first.PID {
		t.Fatal("new consent did not replace old watcher")
	}
	if out, err := run("stop"); err != nil {
		t.Fatalf("stop: %s %v", out, err)
	}
	if _, live, err := runningWatch(root); err != nil || live {
		t.Fatalf("still running: %v %v", live, err)
	}
	if out, err := run("start"); err == nil || !strings.Contains(out, "paused") {
		t.Fatalf("pause ignored: %s %v", out, err)
	}
	if out, err := run("start", "--resume"); err != nil {
		t.Fatalf("resume: %s %v", out, err)
	}
	// Per-repository revocation cancels the managed watcher and preserves
	// consent for the other repository without trusting a saved PID.
	if out, err := run("revoke", repo); err != nil {
		t.Fatalf("revoke: %s %v", out, err)
	}
	var cfg watching.Config
	if err := watching.Read(file, &cfg); err != nil {
		t.Fatal(err)
	}
	if watching.Allowed(cfg, repo) || !watching.Allowed(cfg, other) {
		t.Fatal("revocation scope incorrect")
	}
	if out, err := run("start", "--resume", "--no-browser"); err == nil {
		t.Fatalf("revoked consent reused: %s", out)
	}
	if requests.Load() != 0 {
		t.Fatalf("setup or idle watcher contacted service %d times without an agent edit", requests.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitWatch(ctx, func() (bool, error) { _, live, err := runningWatch(root); return !live, err }); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultWatchSetupScopesToCurrentRepository(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	repo := newWatchTestRepo(t)
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	api, profile := fakeSetupAPI(t), "dev"
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	c := newWatchCommand(app, &api, &profile)
	c.SetContext(context.Background())
	c.SetOut(io.Discard)
	_ = c.Flags().Set("browser", "true")
	fakeSetupBrowser(t, "allow", nil)
	cfg, _, err := prepareWatch(c, app, &api, &profile, nil)
	if err != nil || cfg.AllRepositories || len(cfg.Directories) != 1 || cfg.Directories[0] != repo {
		t.Fatalf("default scope: %+v %v", cfg, err)
	}
}

func TestWatchLifecycleCommandsInProcess(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	api, profile := "http://localhost:3000/api", "dev"
	repo := newWatchTestRepo(t)
	c := newWatchCommand(app, &api, &profile)
	c.SetContext(context.Background())
	c.SetOut(io.Discard)
	_ = c.Flags().Set("yes", "true")
	cfg, _, err := prepareWatch(c, app, &api, &profile, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	root, _, _ := watchPaths(api, profile)
	run := func(name string, args ...string) error {
		command, _, err := c.Find([]string{name})
		if err != nil {
			return err
		}
		command.SetContext(context.Background())
		return command.RunE(command, args)
	}
	// Reuse a live watcher without spawning a subprocess from the test binary.
	lock, err := publock.TryAcquire(root, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	if err := watching.Write(filepath.Join(root, "process.json"), watchProcess{PID: os.Getpid(), ConsentedAt: cfg.ConsentedAt}); err != nil {
		t.Fatal(err)
	}
	if err := watching.Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := run("start", repo); err != nil {
		t.Fatal(err)
	}
	if err := run("status", repo); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run("stop"); err != nil {
		t.Fatal(err)
	}
	if err := run("start", repo); err == nil {
		t.Fatal("start ignored pause")
	}
	if err := run("revoke", repo); err != nil {
		t.Fatal(err)
	}
	if err := run("status", repo); err != nil {
		t.Fatal(err)
	}
}

func TestConsentCannotRestoreApprovalChangedWhileBrowserWasOpen(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	api, profile := fakeSetupAPI(t), "dev"
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	repo := newWatchTestRepo(t)
	_, file, _ := watchPaths(api, profile)
	replacement := watching.Config{Version: 1, API: api, Profile: profile, ConsentedAt: time.Now()}
	fakeSetupBrowser(t, "allow", func(_ string) {
		// Simulate a separate user revoking/changing approval while the prompt is open.
		if err := watching.Write(file, replacement); err != nil {
			t.Fatal(err)
		}
	})
	c := newWatchCommand(app, &api, &profile)
	c.SetContext(context.Background())
	c.SetOut(io.Discard)
	_ = c.Flags().Set("browser", "true")
	if _, _, err := prepareWatch(c, app, &api, &profile, []string{repo}); err == nil || !strings.Contains(err.Error(), "changed during setup") {
		t.Fatalf("stale approval accepted: %v", err)
	}
	var saved watching.Config
	if err := watching.Read(file, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.ConsentedAt.Equal(replacement.ConsentedAt) || len(saved.Directories) != 0 {
		t.Fatal("concurrent revocation was overwritten")
	}
}

func TestRefreshManagedWatcherPreservesConsentAndUserPause(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config.json")
	original := []byte(`{"consent":"unchanged"}`)
	if err := os.WriteFile(cfg, original, 0600); err != nil {
		t.Fatal(err)
	}
	// Already-paused state is never cleared by an upgrade.
	if err := watching.Write(filepath.Join(root, "paused.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshManagedWatch(context.Background(), root, func(watchProcess) error { t.Fatal("started paused watcher"); return nil }); err == nil {
		t.Fatal("resumed paused watcher")
	}
	if !paused(root) {
		t.Fatal("removed user pause")
	}
	if err := os.Remove(filepath.Join(root, "paused.json")); err != nil {
		t.Fatal(err)
	}
	lock, err := publock.TryAcquire(root, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	var closeOnce sync.Once
	closeLock := func() { closeOnce.Do(func() { _ = lock.Close() }) }
	t.Cleanup(closeLock)
	if err := watching.Write(filepath.Join(root, "process.json"), watchProcess{WorkingDirectory: root, ManagedSession: "managed", ConsentedAt: time.Now(), Directories: []string{"original-directory"}}); err != nil {
		t.Fatal(err)
	}
	if err := watching.Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	released, stop := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(released)
		defer closeLock()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if paused(root) {
					return
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-released })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := false
	refreshed, err := refreshManagedWatch(ctx, root, func(previous watchProcess) error {
		started = true
		if previous.WorkingDirectory != root {
			t.Error("replacement cwd did not come from original selection")
		}
		if len(previous.Directories) != 1 || previous.Directories[0] != "original-directory" {
			t.Error("lost watcher selection")
		}
		return nil
	})
	if err != nil || !refreshed || !started {
		t.Fatalf("refreshed=%t started=%t err=%v", refreshed, started, err)
	}
	<-released
	if paused(root) {
		t.Fatal("left refresh pause behind")
	}
	data, err := os.ReadFile(cfg)
	if err != nil || string(data) != string(original) {
		t.Fatal("changed consent")
	}
}

func TestRefreshWorkingDirectoryUsesRecordedSelection(t *testing.T) {
	original := t.TempDir()
	got, err := refreshWorkingDirectory(watchProcess{WorkingDirectory: original, Directories: []string{t.TempDir()}})
	if err != nil || got != original {
		t.Fatalf("cwd=%q err=%v want=%q", got, err, original)
	}
}

func TestRefreshInvalidDirectoryLeavesWatcherRunning(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "regular-file")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, directory string }{
		{"old record", ""}, {"deleted directory", filepath.Join(root, "deleted")},
		{"regular file", file}, {"relative path", "relative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lock, err := publock.TryAcquire(root, "watcher")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lock.Close() })
			// An approved seed/selection is not a substitute for the actual cwd.
			if err := watching.Write(filepath.Join(root, "config.json"), watching.Config{Directories: []string{root}}); err != nil {
				t.Fatal(err)
			}
			if err := watching.Write(filepath.Join(root, "process.json"), watchProcess{ManagedSession: "managed", WorkingDirectory: tc.directory, Directories: []string{root}}); err != nil {
				t.Fatal(err)
			}
			if err := watching.Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := refreshManagedWatch(context.Background(), root, func(watchProcess) error { t.Error("started replacement"); return nil }); err == nil {
				t.Fatal("accepted invalid directory")
			}
			if paused(root) {
				t.Fatal("paused original watcher")
			}
			if _, live, err := runningWatch(root); err != nil || !live {
				t.Fatalf("original watcher lost: live=%t err=%v", live, err)
			}
		})
	}
}

func TestWatchStatusCommandPreservesLocalState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission modes")
	}
	dir := t.TempDir()
	t.Setenv("DOOMER_CONFIG_DIR", dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"auths":{}}`), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	root, _, err := watchPaths("https://doomer.ai/api", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"--profile", "default", "--api-url", "https://doomer.ai/api", "watch", "status", "--json", newWatchTestRepo(t)})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0750, config: 0640} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("status changed %s: %v", p, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("status created watcher state: %v %v", entries, err)
	}
}
