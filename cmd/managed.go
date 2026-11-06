package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/paths"
	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/spf13/cobra"
)

type managedResult struct {
	State                string                         `json:"state"`
	Task                 string                         `json:"task,omitempty"`
	Release              string                         `json:"release,omitempty"`
	Instructions         string                         `json:"instructions,omitempty"`
	Review               *adversarylabs.WorkspaceReview `json:"review,omitempty"`
	URL                  string                         `json:"url,omitempty"`
	Action               string                         `json:"action,omitempty"`
	Error                string                         `json:"error,omitempty"`
	SoftwareReady        bool                           `json:"software_ready"`
	Authenticated        bool                           `json:"authenticated"`
	UploadApproved       bool                           `json:"upload_approved"`
	HookActivated        bool                           `json:"hook_activated"`
	Responses            map[string]managed.Response    `json:"responses,omitempty"`
	Deferred             int                            `json:"deferred"`
	UpdateAvailable      string                         `json:"update_available,omitempty"`
	PromptTruncated      bool                           `json:"prompt_truncated"`
	SubmittedPromptRunes int                            `json:"submitted_prompt_runes"`
	Capabilities         managed.Capabilities           `json:"capabilities"`
}

func managedStore() (managed.Store, error) {
	dir := os.Getenv("DOOMER_CONFIG_DIR")
	if dir == "" {
		var err error
		dir, err = paths.ConfigDir()
		if err != nil {
			return managed.Store{}, err
		}
	}
	s := managed.Store{Root: filepath.Join(dir, "managed")}
	return s, s.Init()
}
func resultFor(task managed.Task) managedResult {
	r := managedResult{State: task.Stage, Task: task.Handle, Release: task.Release, SoftwareReady: task.Release != "", HookActivated: task.HookActivated, Review: task.Review}
	r.PromptTruncated = len([]rune(task.Prompt)) > 4000
	r.SubmittedPromptRunes = min(len([]rune(task.Prompt)), 4000)
	r.Capabilities = task.Capabilities
	r.UpdateAvailable = task.OfferedRelease
	if task.Review != nil {
		r.Responses = map[string]managed.Response{}
		for key, response := range task.Responses {
			if strings.HasPrefix(key, task.Review.ID+":") {
				r.Responses[key] = response
				if response.Sent && response.Status == "deferred" {
					r.Deferred++
				}
			}
		}
		r.URL = adversarylabs.WorkspaceLink(task.API, *task.Review)
	}
	return r
}
func outputManaged(c *cobra.Command, s managed.Store, t managed.Task, r managedResult) error {
	// Operation outcomes still describe the same pinned runtime and task.
	taskResult := resultFor(t)
	r.Task = taskResult.Task
	r.Release = taskResult.Release
	r.SoftwareReady = taskResult.SoftwareReady
	r.HookActivated = taskResult.HookActivated
	r.Capabilities = taskResult.Capabilities
	r.PromptTruncated = taskResult.PromptTruncated
	r.SubmittedPromptRunes = taskResult.SubmittedPromptRunes
	r.UpdateAvailable = taskResult.UpdateAvailable
	r.Review = taskResult.Review
	r.Responses = taskResult.Responses
	r.Deferred = taskResult.Deferred
	if r.URL == "" {
		r.URL = taskResult.URL
	}
	if t.Handle != "" {
		if err := s.Observe(t, r.State); err != nil {
			return err
		}
	}
	reportManagedStatus(c.Context(), s, t, r.State)
	return json.NewEncoder(c.OutOrStdout()).Encode(r)
}

func newPrepareCommand(app *application.App, api, profile *string) *cobra.Command {
	var repo, agent, handle, prompt, session string
	var allowUpdates bool
	c := &cobra.Command{Use: "prepare", Short: "Prepare or resume a version-pinned managed review task", Args: cobra.NoArgs}
	c.RunE = func(c *cobra.Command, _ []string) error {
		s, err := managedStore()
		if err != nil {
			return err
		}
		var task managed.Task
		if handle != "" {
			task, err = s.Load(handle)
			if err != nil {
				return err
			}
		} else {
			root, e := watch.Root(c.Context(), repo)
			if e != nil {
				return e
			}
			home, e := os.UserHomeDir()
			if e != nil {
				return e
			}
			bootstrap, e := managed.ResolveBootstrap(root, home, agent)
			if e != nil {
				return e
			}
			id, e := watch.RepositoryIdentity(c.Context(), root)
			if e != nil {
				return e
			}
			handle, e = managed.NewHandle()
			if e != nil {
				return e
			}
			runtimeHome, e := managedRuntimeHome()
			if e != nil {
				return e
			}
			task = managed.Task{RuntimeHome: runtimeHome, Schema: managed.Protocol, Handle: handle, Repo: root, RepositoryID: id, API: *api, Profile: *profile, Agent: agent, Bootstrap: bootstrap, Prompt: prompt, Created: time.Now().UTC(), Stage: "preparing", Capabilities: managed.Capabilities{DurableStorage: true, SessionID: session, Hooks: session != ""}}
			// Capture the task baseline before any edits. The task retains this commit
			// even if HEAD advances later or another process resumes the task.
			base, e := exec.CommandContext(c.Context(), "git", "-C", root, "rev-parse", "HEAD").Output()
			if e != nil {
				return e
			}
			task.Base = strings.TrimSpace(string(base))
			if len(session) > 512 || len(prompt) > 16000 {
				return fmt.Errorf("session or task prompt is too large")
			}
			if e = s.Save(&task); e != nil {
				return e
			}
			if e = bindManagedSession(s, task); e != nil {
				return e
			}
		}
		lock, err := s.Lock(handle)
		if err != nil {
			return err
		}
		defer lock.Close()
		task, err = s.Load(handle)
		if err != nil {
			return err
		}
		if task.API != *api || task.Profile != *profile {
			return fmt.Errorf("resume with the original service and profile")
		}
		r := resultFor(task)
		if task.Paused {
			r.State = "paused"
			return outputManaged(c, s, task, r)
		}
		if task.Release == "" {
			state, err := prepareRelease(c.Context(), s, &task, allowUpdates)
			if err != nil {
				r.State = "software_unavailable"
				r.Error = err.Error()
				return outputManaged(c, s, task, r)
			}
			if state != "ready" {
				r.State = state
				r.Action = "Ask the user to allow verified software updates under this bootstrap policy and reporting of version/task-state metadata to " + displayManagedService(task.API) + ". Repository content requires separate upload consent. After approval, resume with --allow-updates"
				return outputManaged(c, s, task, r)
			}
			r = resultFor(task)
			home, err := taskRuntimeHome(task)
			if err != nil {
				return err
			}
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			if executable != runtimeversion.Binary(home, task.Release) {
				lock.Close()
				_, err := dispatchManaged(c.Context(), []string{"--api-url", task.API, "--profile", task.Profile, "prepare", "--task", task.Handle}, c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr())
				return err
			}
		}
		return managedApproval(c, app, s, &task)
	}
	c.Flags().StringVar(&repo, "repo", ".", "checkout being edited")
	c.Flags().StringVar(&agent, "agent", "codex", "active agent: codex or claude")
	c.Flags().StringVar(&handle, "task", "", "resume an existing task handle")
	c.Flags().StringVar(&session, "session", "", "actual harness session ID for optional hook binding")
	c.Flags().StringVar(&prompt, "prompt", "", "original user task (stored locally until upload is approved)")
	c.Flags().BoolVar(&allowUpdates, "allow-updates", false, "record the user's explicit authorization for this bootstrap's update policy")
	return c
}

type selectedRelease struct {
	Release    managed.Release `json:"release"`
	Policy     managed.Policy  `json:"policy"`
	Authorized bool            `json:"authorized"`
	Previous   managed.Release `json:"previous"`
	Rejected   map[string]bool `json:"rejected,omitempty"`
}

func prepareRelease(ctx context.Context, s managed.Store, task *managed.Task, allow bool) (string, error) {
	home, err := taskRuntimeHome(*task)
	if err != nil {
		return "", err
	}
	key := watch.Key(task.Bootstrap.Path)
	lock, err := watchLock(s.Root, "release-"+key)
	if err != nil {
		return "", err
	}
	defer lock()
	file := filepath.Join(s.Root, "selections", key+".json")
	var saved selectedRelease
	if err := watch.Read(file, &saved); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	current, err := managed.ReadBootstrap(task.Bootstrap.Path, task.Bootstrap.Scope)
	if err != nil {
		return "", err
	}
	if current.Policy != task.Bootstrap.Policy {
		return "", fmt.Errorf("bootstrap policy changed before activation; create a task for the new policy")
	}
	policy := task.Bootstrap.Policy
	if saved.Policy != policy {
		saved = selectedRelease{Policy: policy}
	}
	if allow {
		saved.Authorized = true
		if err := watch.Write(file, saved); err != nil {
			return "", err
		}
	}
	if !saved.Authorized {
		return "update_permission_required", nil
	}
	request, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	catalog, fetchErr := managed.FetchCatalog(request, &http.Client{Timeout: 15 * time.Second})
	selected := saved.Release
	if fetchErr == nil {
		eligible := catalog
		eligible.Releases = nil
		for _, release := range catalog.Releases {
			if !saved.Rejected[release.Version] {
				eligible.Releases = append(eligible.Releases, release)
			}
		}
		candidate, e := managed.SelectRelease(eligible, policy, time.Now())
		if e != nil {
			return "", e
		}
		if policy.Channel == "manual" && saved.Release.Version != "" && saved.Release.Version != candidate.Version && !allow {
			selected = saved.Release
			task.OfferedRelease = candidate.Version
		} else {
			selected = candidate
			if saved.Release.Version != "" && managed.VersionLess(selected.Version, saved.Release.Version) && policy.Channel != "pinned" {
				selected = saved.Release
			}
		}
		for _, release := range catalog.Releases {
			if release.Version == selected.Version && release.Withdrawn {
				return "", fmt.Errorf("selected release was withdrawn; choose an eligible replacement")
			}
		}
	} else if selected.Version == "" {
		return "", fetchErr
	}
	if selected.Version == "" {
		return "", fmt.Errorf("no selected release")
	}
	updater := selfupdate.Updater{Home: home, Versioned: true, ExpectedManifestSHA256: selected.ManifestSHA256}
	status, err := updater.Check(selected.Version)
	if err != nil {
		return "", err
	}
	if status.State != "ready" {
		if fetchErr != nil {
			return "", fetchErr
		}
		status, err = updater.Apply(ctx, selected.Version, false)
		if err != nil {
			return "", managed.NetworkError("release_install", err)
		}
	}
	if status.State != "ready" {
		return "", fmt.Errorf("selected release failed verification")
	}
	// The release's embedded protocol must match before any user task executes.
	output, err := exec.CommandContext(ctx, status.Path, "runtime-info").Output()
	var info struct {
		ManagedProtocol int `json:"managed_protocol"`
	}
	if err != nil || json.Unmarshal(output, &info) != nil || info.ManagedProtocol != managed.Protocol {
		return "", fmt.Errorf("release does not implement managed protocol %d", managed.Protocol)
	}
	// Only advance the launcher after the candidate protocol was verified.
	launcher := updater
	launcher.Versioned = false
	existing, checkErr := launcher.Check(selected.Version)
	if checkErr != nil {
		return "", checkErr
	}
	if existing.State == "missing" || existing.Managed && existing.State == "update_available" {
		if _, err = updater.InstallLauncher(selected.Version, false); err != nil {
			return "", err
		}
	}
	if saved.Release.Version != "" && saved.Release.Version != selected.Version {
		saved.Previous = saved.Release
	}
	saved.Release = selected
	if err = watch.Write(file, saved); err != nil {
		return "", err
	}
	task.Release = selected.Version
	return "ready", s.Save(task)
}
func watchLock(root, key string) (func(), error) {
	lock, err := publock.TryAcquire(root, key)
	if err != nil {
		return nil, err
	}
	return func() { _ = lock.Close() }, nil
}

func managedIdentity(ctx context.Context, app *application.App, t managed.Task) (adversarylabs.Auth, string, error) {
	auth, ok, err := scopedAuth(app.Dependencies().Auth, t.API, t.Profile, app.Dependencies().RegistryHost)
	if err != nil {
		return auth, "", err
	}
	if !ok || auth.Token == "" || adversarylabs.IsAnonymousToken(auth.Token) {
		return auth, "", fmt.Errorf("account login required")
	}
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, t.API)
	account, err := client.Whoami(ctx, auth.Token)
	if err != nil {
		return auth, "", managed.NetworkError("account_verification", err)
	}
	if account.ID == "" {
		return auth, "", fmt.Errorf("account identity missing")
	}
	return auth, account.ID, nil
}

type repositoryConsent struct {
	Account, Repository, API, Profile string
	Approved                          bool
	At                                time.Time
}

func managedConsent(s managed.Store, t managed.Task, token string, allow bool) (bool, error) {
	// The grant follows the authenticated account, service, profile, and Git
	// common directory. A refreshed credential for the same account can resume.
	key := watch.Key(t.AccountID + "\x00" + t.RepositoryID + "\x00" + t.API + "\x00" + t.Profile)
	file := filepath.Join(s.Root, "consents", key+".json")
	var grant repositoryConsent
	if allow {
		grant = repositoryConsent{t.AccountID, t.RepositoryID, t.API, t.Profile, true, time.Now().UTC()}
		return true, watch.Write(file, grant)
	}
	err := watch.Read(file, &grant)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return grant.Approved && grant.Account == t.AccountID && grant.Repository == t.RepositoryID && grant.API == t.API && grant.Profile == t.Profile, nil
}

func newManagedTaskCommand(app *application.App, api, profile *string) *cobra.Command {
	parent := &cobra.Command{Use: "task", Short: "Resume a durable managed review without a persistent process"}
	for _, operation := range []string{"submit", "status", "respond", "finish", "consent", "revoke", "login", "hook", "pause", "resume", "rollback", "retry", "repair", "context"} {
		operation := operation
		var allow bool
		c := &cobra.Command{Use: operation + " HANDLE", Args: cobra.ExactArgs(1)}
		c.Flags().BoolVar(&allow, "allow", false, "record an explicit user approval; never supply on the user's behalf")
		c.RunE = func(c *cobra.Command, args []string) error {
			s, err := managedStore()
			if err != nil {
				return err
			}
			lock, err := s.Lock(args[0])
			if err != nil {
				return err
			}
			defer lock.Close()
			t, err := s.Load(args[0])
			if err != nil {
				return err
			}
			if t.API != *api || t.Profile != *profile {
				return fmt.Errorf("task service or profile mismatch")
			}
			if operation == "repair" {
				if err := repairManagedRelease(c.Context(), t); err != nil {
					return err
				}
				return outputManaged(c, s, t, managedResult{State: "runtime_repaired", Task: t.Handle, Release: t.Release, SoftwareReady: true})
			}
			if operation == "context" {
				if t.Submission != nil {
					return fmt.Errorf("submitted task context is immutable")
				}
				data, err := io.ReadAll(io.LimitReader(c.InOrStdin(), 16001))
				if err != nil {
					return err
				}
				if len(data) > 16000 || len([]rune(string(data))) > 4000 || strings.TrimSpace(string(data)) == "" {
					return fmt.Errorf("provide a task summary of at most 4000 characters")
				}
				t.Prompt = string(data)
				if err = s.Save(&t); err != nil {
					return err
				}
				return outputManaged(c, s, t, resultFor(t))
			}
			if operation == "rollback" {
				if err := rollbackManaged(s, t); err != nil {
					return err
				}
				return outputManaged(c, s, t, managedResult{State: "rollback_recorded", Task: t.Handle, Action: "Start a new task to use the prior verified release. This active task keeps its existing version."})
			}
			if operation == "pause" || operation == "resume" {
				t.Paused = operation == "pause"
				if err = s.Save(&t); err != nil {
					return err
				}
				state := "resumed"
				if t.Paused {
					state = "paused"
				}
				return outputManaged(c, s, t, managedResult{State: state, Task: t.Handle})
			}
			if t.Paused {
				return outputManaged(c, s, t, managedResult{State: "paused", Task: t.Handle})
			}
			if operation == "login" {
				return managedLogin(c, app, s, t)
			}
			auth, account, err := managedIdentity(c.Context(), app, t)
			if err != nil {
				return outputManaged(c, s, t, managedResult{State: "authentication_required", Task: t.Handle, Error: err.Error()})
			}
			if t.AccountID != "" && t.AccountID != account {
				return fmt.Errorf("task account changed; original account login required")
			}
			t.AccountID = account
			if operation == "consent" {
				if !allow {
					return fmt.Errorf("--allow requires the user's explicit approval of repository snapshots, task context and finding response uploads to %s", t.API)
				}
				if _, err = managedConsent(s, t, auth.Token, true); err != nil {
					return err
				}
				if err = s.Save(&t); err != nil {
					return err
				}
				return outputManaged(c, s, t, managedResult{State: "consent_recorded", Task: t.Handle, Authenticated: true, UploadApproved: true})
			}
			if operation == "revoke" {
				key := watch.Key(t.AccountID + "\x00" + t.RepositoryID + "\x00" + t.API + "\x00" + t.Profile)
				if err := watch.Write(filepath.Join(s.Root, "consents", key+".json"), repositoryConsent{Account: t.AccountID}); err != nil {
					return err
				}
				return outputManaged(c, s, t, managedResult{State: "consent_revoked", Task: t.Handle, Authenticated: true})
			}
			approved, err := managedConsent(s, t, auth.Token, false)
			if err != nil {
				return err
			}
			if !approved {
				return outputManaged(c, s, t, managedResult{State: "consent_required", Task: t.Handle, Authenticated: true})
			}
			actual, err := watch.RepositoryIdentity(c.Context(), t.Repo)
			if err != nil || actual != t.RepositoryID {
				return fmt.Errorf("task checkout identity changed")
			}
			if t.Release == "" {
				return fmt.Errorf("resume prepare to install the selected release")
			}
			engine := managedEngine(s, t)
			switch operation {
			case "submit":
				err = engine.Submit(c.Context(), &t, auth.Token)
			case "retry":
				err = engine.Retry(c.Context(), &t, auth.Token)
			case "status":
				err = engine.Status(c.Context(), &t, auth.Token)
			case "finish":
				err = engine.Finish(c.Context(), &t, auth.Token)
			case "respond":
				var items []managed.Response
				decoder := json.NewDecoder(io.LimitReader(c.InOrStdin(), 256<<10))
				decoder.DisallowUnknownFields()
				if err = decoder.Decode(&items); err == nil {
					if decoder.Decode(new(any)) != io.EOF {
						return fmt.Errorf("expected one JSON response array")
					}
					err = engine.Respond(c.Context(), &t, auth.Token, items)
				}
			case "hook":
				err = managedHook(c, s, &t, engine, auth.Token)
			}
			r := resultFor(t)
			r.Authenticated = true
			r.UploadApproved = true
			if err != nil {
				r.State = "operation_failed"
				r.Error = err.Error()
			}
			return outputManaged(c, s, t, r)
		}
		parent.AddCommand(c)
	}
	return parent
}
func managedEngine(s managed.Store, t managed.Task) managed.Engine {
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, t.API)
	return managed.Engine{Store: s, Client: client,
		Capture: func(ctx context.Context, t managed.Task) (sourcesnapshot.Capture, error) {
			return sourcesnapshot.CaptureWorktree(ctx, sourcesnapshot.CaptureOptions{Path: t.Repo, Base: t.Base, Workspace: true})
		},
		Upload: func(ctx context.Context, t managed.Task, token, repository string, manifest sourcesnapshot.Manifest) (string, string, string, error) {
			root, _, err := watchPaths(t.API, t.Profile)
			if err != nil {
				return "", "", "", err
			}
			runner := watch.Runner{Root: root, Config: watch.Config{API: t.API, Profile: t.Profile}, Client: client, Token: token, Output: io.Discard}
			return runner.UploadSnapshots(ctx, t.Repo, repository, manifest)
		}}
}

// Hooks use exactly the same journal and ownership as explicit CLI calls.
// A task handle must be supplied by its adapter; never guess from the newest task.
func managedHook(c *cobra.Command, s managed.Store, t *managed.Task, e managed.Engine, token string) error {
	var event struct {
		Event string `json:"hook_event_name"`
		CWD   string `json:"cwd"`
	}
	if err := json.NewDecoder(io.LimitReader(c.InOrStdin(), 1<<20)).Decode(&event); err != nil {
		return err
	}
	root, err := watch.Root(c.Context(), event.CWD)
	if err != nil || root != t.Repo {
		return fmt.Errorf("hook checkout does not match task")
	}
	t.HookActivated = true
	if err := s.Save(t); err != nil {
		return err
	}
	switch event.Event {
	case "PostToolUse":
		return e.Submit(c.Context(), t, token)
	case "Stop":
		if err := e.Submit(c.Context(), t, token); err != nil {
			return err
		}
		if err := e.Status(c.Context(), t, token); err != nil {
			return err
		}
		return e.Finish(c.Context(), t, token)
	case "UserPromptSubmit", "PreToolUse":
		return nil
	default:
		return fmt.Errorf("unsupported hook event")
	}
}

func managedLogin(c *cobra.Command, app *application.App, s managed.Store, t managed.Task) error {
	return managedApproval(c, app, s, &t)
}

// Explicit task routing precedes checkout policy routing. This is how two tasks
// retain different releases even in the same checkout or after a policy edit.
func dispatchManaged(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (bool, error) {
	root := newRootCommand(nil)
	command, rest, err := root.Find(args)
	if err != nil {
		return false, nil
	}
	var handle string
	if command.Parent() != nil && command.Parent().Name() == "task" {
		if command.Name() == "repair" {
			return false, nil
		}
		if err := command.ParseFlags(rest); err != nil {
			return true, err
		}
		values := command.Flags().Args()
		if len(values) == 1 {
			handle = values[0]
		}
	} else if command.Name() == "prepare" {
		if err := command.ParseFlags(rest); err != nil {
			return true, err
		}
		handle, _ = command.Flags().GetString("task")
	}
	if handle == "" {
		return false, nil
	}
	s, err := managedStore()
	if err != nil {
		return true, err
	}
	task, err := s.Load(handle)
	if err != nil {
		return true, err
	}
	if task.Release == "" {
		return false, nil
	}
	home, err := taskRuntimeHome(task)
	if err != nil {
		return true, err
	}
	binary := runtimeversion.Binary(home, task.Release)
	current, err := os.Executable()
	if err != nil {
		return true, err
	}
	if current == binary {
		return false, nil
	}
	status, err := (selfupdate.Updater{Home: home, Versioned: true}).Check(task.Release)
	if err != nil || status.State != "ready" {
		return true, fmt.Errorf("task release %s is unavailable or modified", task.Release)
	}
	child := exec.CommandContext(ctx, binary, args...)
	child.Stdin = in
	child.Stdout = out
	child.Stderr = stderr
	return true, child.Run()
}

func managedInstructionDigest() string {
	sum := sha256.Sum256([]byte(managed.Instructions))
	return hex.EncodeToString(sum[:])
}

func rollbackManaged(s managed.Store, t managed.Task) error {
	key := watch.Key(t.Bootstrap.Path)
	unlock, err := watchLock(s.Root, "release-"+key)
	if err != nil {
		return err
	}
	defer unlock()
	file := filepath.Join(s.Root, "selections", key+".json")
	var selection selectedRelease
	if err := watch.Read(file, &selection); err != nil {
		return err
	}
	if selection.Policy.Channel == "pinned" {
		return fmt.Errorf("change the repository pin explicitly to select a different release")
	}
	if selection.Release.Version != t.Release {
		return fmt.Errorf("selected release changed; rollback requires a task on the current selection")
	}
	if selection.Previous.Version == "" {
		return fmt.Errorf("no previous verified release is available")
	}
	home, err := taskRuntimeHome(t)
	if err != nil {
		return err
	}
	status, err := (selfupdate.Updater{Home: home, Versioned: true}).Check(selection.Previous.Version)
	if err != nil || status.State != "ready" {
		return fmt.Errorf("previous release is unavailable or modified")
	}
	if selection.Rejected == nil {
		selection.Rejected = map[string]bool{}
	}
	selection.Rejected[selection.Release.Version] = true
	selection.Release = selection.Previous
	selection.Previous = managed.Release{}
	return watch.Write(file, selection)
}

// Status delivery is best effort and separately bounded. The private observation
// remains available when the service is unreachable. No review is created here.
func reportManagedStatus(ctx context.Context, s managed.Store, t managed.Task, state string) {
	if t.Release == "" || t.AccountID == "" {
		return
	}
	store := adversarylabs.ConfigStore{Path: filepath.Join(filepath.Dir(s.Root), "config.json")}
	auth, ok, err := store.StoredAuthE(adversarylabs.AuthKey(t.API, t.Profile))
	if err != nil || !ok || auth.Token == "" || adversarylabs.IsAnonymousToken(auth.Token) {
		return
	}
	installation, err := watch.EnsureInstallationID(filepath.Join(s.Root, "identities", watch.Key(t.API+"\x00"+t.Profile)))
	if err != nil {
		return
	}
	var sequence int64
	if err := watch.Locked(s.Root, "observation-"+t.Handle, func() error {
		path := filepath.Join(s.Root, "observation-sequences", t.Handle+".json")
		if err := watch.Read(path, &sequence); err != nil && !os.IsNotExist(err) {
			return err
		}
		sequence++
		return watch.Write(path, sequence)
	}); err != nil {
		return
	}
	observation := adversarylabs.ManagedStatus{InstallationID: installation, Repository: watch.Key(t.RepositoryID), Task: t.Handle, Agent: t.Agent, Release: t.Release, Protocol: managed.Protocol, Policy: t.Bootstrap.Policy.Channel, State: state, Sequence: sequence, Paused: t.Paused, HookActivated: t.HookActivated}
	timeout, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, t.API)
	_ = client.ReportManagedStatus(timeout, auth.Token, observation)
}

// Repair restores the exact immutable release recorded in the task. It never
// resolves a newer channel or changes bootstrap policy or the task's version.
func repairManagedRelease(ctx context.Context, t managed.Task) error {
	if !runtimeversion.Release.MatchString(t.Release) {
		return fmt.Errorf("task has no valid pinned release")
	}
	catalog, err := managed.FetchCatalog(ctx, &http.Client{Timeout: 15 * time.Second})
	if err != nil {
		return err
	}
	var pinned *managed.Release
	for _, release := range catalog.Releases {
		if release.Version == t.Release && !release.Withdrawn && release.Protocol == managed.Protocol {
			copy := release
			pinned = &copy
		}
	}
	if pinned == nil {
		return fmt.Errorf("task's pinned release is unavailable or withdrawn")
	}
	home, err := taskRuntimeHome(t)
	if err != nil {
		return err
	}
	updater := selfupdate.Updater{Home: home, Versioned: true, ExpectedManifestSHA256: pinned.ManifestSHA256}
	status, err := updater.Apply(ctx, pinned.Version, false)
	if err != nil {
		return err
	}
	if status.State != "ready" {
		return fmt.Errorf("pinned runtime repair did not complete")
	}
	_, err = updater.InstallLauncher(pinned.Version, false)
	return err
}

// A separate runtime root supports isolated validation and ephemeral harnesses
// without changing the process home or the user's existing CLI installation.
func managedRuntimeHome() (string, error) {
	if root := os.Getenv("DOOMER_RUNTIME_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("DOOMER_RUNTIME_HOME must be absolute")
		}
		root = filepath.Clean(root)
		if err := os.MkdirAll(root, 0700); err != nil {
			return "", err
		}
		info, err := os.Lstat(root)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("runtime root must be a regular directory")
		}
		return root, nil
	}
	return os.UserHomeDir()
}

func taskRuntimeHome(task managed.Task) (string, error) {
	if task.RuntimeHome != "" {
		if !filepath.IsAbs(task.RuntimeHome) {
			return "", fmt.Errorf("task runtime root must be absolute")
		}
		return task.RuntimeHome, nil
	}
	return managedRuntimeHome()
}
