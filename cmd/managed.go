package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	NetworkAccess        *managedNetworkAccess          `json:"network_access,omitempty"`
	Anonymous            bool                           `json:"anonymous,omitempty"`
	ReviewNotRequired    string                         `json:"review_not_required,omitempty"`
	NoChanges            bool                           `json:"no_changes,omitempty"`
	CodexPolicyStatus    *codexPolicyStatus             `json:"codex_policy_status,omitempty"`
	CodexReviewPolicy    *codexReviewPolicy             `json:"codex_review_policy,omitempty"`
	BootstrapUpdates     []managed.BootstrapUpdate      `json:"bootstrap_updates,omitempty"`
	Consent              *managedConsentEvidence        `json:"consent,omitempty"`
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
	PortableSetupCommand []string                       `json:"portable_setup_command,omitempty"`
}

func managedStore() (managed.Store, error) {
	s, err := managedStoreLocation()
	if err != nil {
		return s, err
	}
	if _, err := os.Stat(filepath.Join(s.Root, "reset-in-progress")); err == nil {
		return s, fmt.Errorf("onboarding reset is in progress; retry after it completes")
	} else if !os.IsNotExist(err) {
		return s, err
	}
	return s, s.Init()
}

// Read-only consent checks must not create or chmod the managed state directory.
func managedStoreLocation() (managed.Store, error) {
	dir := os.Getenv("DOOMER_CONFIG_DIR")
	if dir == "" {
		var err error
		dir, err = paths.ConfigDir()
		if err != nil {
			return managed.Store{}, err
		}
	}
	s := managed.Store{Root: filepath.Join(dir, "managed")}
	return s, nil
}
func resultFor(task managed.Task) managedResult {
	r := managedResult{Error: task.LastError, State: task.Stage, Task: task.Handle, Release: task.Release, SoftwareReady: task.Release != "", HookActivated: task.HookActivated, Review: task.Review}
	r.Anonymous = strings.HasPrefix(task.AccountID, "anonymous:")
	if task.Stage == "reviewed" && task.LastError == "" && task.Review != nil {
		r.Action = "The review field contains this task's completed results. Record any outstanding finding responses, then run doomer task finish " + task.Handle + "."
	}
	r.PromptTruncated = len([]rune(task.Prompt)) > 4000
	r.NoChanges = task.NoChanges
	if task.Purpose == "setup" && task.Stage == "complete" {
		r.ReviewNotRequired = "setup_only"
	}
	r.SubmittedPromptRunes = min(len([]rune(task.Prompt)), 4000)
	r.Capabilities = task.Capabilities
	r.UpdateAvailable = task.OfferedRelease
	if task.Release != "" && !task.Portable {
		if binary, err := managedTaskBinary(task); err == nil {
			r.PortableSetupCommand = []string{binary, "--api-url", task.API, "--profile", task.Profile, "prepare", "--task", task.Handle, "--portable"}
			if task.Release == "dev" {
				r.PortableSetupCommand = append([]string{"env", "DOOMER_DEV_CLI=1"}, r.PortableSetupCommand...)
			}
		}
	}
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
func managedTaskInstructions(task managed.Task) string {
	instructions := managed.Instructions
	if task.Purpose == "setup" {
		instructions = "This is a setup-only task. Configure hooks for the active agent with `doomer hooks --agent " + task.Agent + "` and verify readiness. If a hook asks for a session binding, resume this same setup using `doomer prepare --task " + task.Handle + " --session ACTUAL_SESSION_ID` from the hook. Preserve native hook trust controls. Do not submit repository content or run a code review for this task. The coding review procedure below applies only to coding tasks.\n\n" + instructions
	}
	if strings.HasPrefix(task.AccountID, "anonymous:") {
		instructions = "This task uses an anonymous capability token. authenticated=true confirms that credential, not an account sign-in. The saved repository upload consent applies to this anonymous installation. Continue through the same task commands without requiring sign-in. Anonymous reviews are quick looks only.\n\n" + instructions
	}
	return instructions
}

func outputManaged(c *cobra.Command, s managed.Store, t managed.Task, r managedResult) error {
	if c.Name() == "prepare" && t.Bootstrap.Path != "" {
		r.BootstrapUpdates = managedBootstrapUpdates(t)
	}
	if r.Authenticated && r.UploadApproved {
		evidence, err := savedManagedConsentEvidence(s, t)
		if err != nil {
			return err
		}
		r.Consent = evidence
		if evidence != nil && t.Agent == "codex" {
			policy, _, inspectErr := inspectCodexReviewPolicy(*evidence)
			r.CodexPolicyStatus = &codexPolicyStatus{State: "unreadable", Activation: "unverified"}
			if inspectErr != nil {
				r.CodexPolicyStatus.Error = inspectErr.Error()
			} else {
				r.CodexPolicyStatus.ConfigPath = policy.ConfigPath
				r.CodexPolicyStatus.State = policy.State
			}
		}
		if evidence != nil && t.Agent == "codex" && r.CodexPolicyStatus.Error == "" && (c.Name() == "prepare" || c.Name() == "login" || c.Name() == "consent" || c.Name() == "codex-policy") {
			if c.Name() == "codex-policy" {
				// An explicit inspection must remain able to recover a proposal that
				// an older chat displayed but never installed.
				r.CodexReviewPolicy, _, err = inspectCodexReviewPolicy(*evidence)
				if err == nil && r.CodexReviewPolicy.State == "configured" {
					r.CodexReviewPolicy = nil
				}
			} else {
				r.CodexReviewPolicy, err = offerCodexReviewPolicy(s, *evidence)
			}
			if err != nil {
				return err
			}
		}
	}
	// Operation outcomes still describe the same pinned runtime and task.
	taskResult := resultFor(t)
	r.Task = taskResult.Task
	r.Release = taskResult.Release
	r.SoftwareReady = taskResult.SoftwareReady
	r.HookActivated = taskResult.HookActivated
	r.Capabilities = taskResult.Capabilities
	r.PromptTruncated = taskResult.PromptTruncated
	r.PortableSetupCommand = taskResult.PortableSetupCommand
	r.SubmittedPromptRunes = taskResult.SubmittedPromptRunes
	r.NoChanges = taskResult.NoChanges
	r.Anonymous = taskResult.Anonymous
	r.ReviewNotRequired = taskResult.ReviewNotRequired
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
	if err := json.NewEncoder(c.OutOrStdout()).Encode(r); err != nil {
		return err
	}
	return markCodexReviewPolicyOffered(s, r.CodexReviewPolicy)
}

func newPrepareCommand(app *application.App, api, profile *string) *cobra.Command {
	var repo, agent, handle, prompt, session, baseRef string
	var allowUpdates bool
	var portable bool
	c := &cobra.Command{Use: "prepare", Short: "Prepare or resume a version-pinned managed review task", Args: cobra.NoArgs}
	c.RunE = func(c *cobra.Command, _ []string) error {
		if handle != "" && c.Flags().Changed("base") {
			return fmt.Errorf("an existing task keeps its original baseline; --base is only for a new task")
		}
		s, err := managedStore()
		if err != nil {
			return err
		}
		if handle == "" && strings.TrimSpace(prompt) == "" && session == "" {
			root, err := watch.Root(c.Context(), repo)
			if err != nil {
				return err
			}
			setupLock, err := publock.TryAcquire(s.Root, "setup-"+watch.Key(*api+"\x00"+*profile+"\x00"+agent+"\x00"+root))
			if err != nil {
				return err
			}
			defer setupLock.Close()
			handle, err = pendingManagedSetup(s, *api, *profile, agent, root)
			if err != nil {
				return err
			}
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
			task = managed.Task{RuntimeHome: runtimeHome, Schema: managed.Protocol, Handle: handle, Repo: root, RepositoryID: id, API: *api, Profile: *profile, Agent: agent, Bootstrap: bootstrap, Prompt: prompt, Portable: portable, Created: time.Now().UTC(), Stage: "preparing", Capabilities: managed.Capabilities{DurableStorage: true, SessionID: session, Hooks: session != ""}}
			if strings.TrimSpace(prompt) == "" {
				task.Purpose = "setup"
			} else {
				task.Purpose = "coding"
			}
			if portable {
				if _, _, err := portableLocation(c.Context(), task); err != nil {
					return err
				}
			}
			// Capture the task baseline before any edits. The task retains this commit
			// even if HEAD advances later or another process resumes the task.
			base, e := exec.CommandContext(c.Context(), "git", "-C", root, "rev-parse", "--verify", "--end-of-options", baseRef+"^{commit}").Output()
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
		if c.Flags().Changed("session") {
			if len(session) == 0 || len(session) > 512 {
				return fmt.Errorf("provide a session ID between 1 and 512 bytes")
			}
			if session != task.Capabilities.SessionID {
				task.Capabilities.SessionID = session
				task.Capabilities.Hooks = true
				if err := s.Save(&task); err != nil {
					return err
				}
			}
			// Reconcile even after a previous call saved the task but failed while
			// updating its derived session index.
			if err := bindManagedSession(s, task); err != nil {
				return err
			}
			if err := clearStaleManagedSessions(s, task); err != nil {
				return err
			}
		}
		if task.Purpose == "setup" && strings.TrimSpace(prompt) != "" {
			if len(prompt) > 16000 {
				return fmt.Errorf("task prompt is too large")
			}
			task.Prompt = prompt
			task.Purpose = "coding"
			if task.Stage == "complete" {
				task.Stage = "ready"
			}
			if err := s.Save(&task); err != nil {
				return err
			}
		}
		if portable && !task.Portable {
			if _, _, err := portableLocation(c.Context(), task); err != nil {
				return err
			}
			task.Portable = true
			if err := s.Save(&task); err != nil {
				return err
			}
		}
		r := resultFor(task)
		if task.Paused {
			r.State = "paused"
			return outputManaged(c, s, task, r)
		}
		if err := restorePortable(c.Context(), app, s, &task); err != nil {
			return outputManagedNetworkError(c, s, task, err)
		}
		r = resultFor(task)
		if task.Release != "dev" && developmentRuntime(task.API, task.Profile) {
			task.Release = "dev"
			if err := s.Save(&task); err != nil {
				return err
			}
		}
		if task.Release == "" {
			state, err := prepareRelease(c.Context(), s, &task, allowUpdates)
			if err != nil {
				r.State = "software_unavailable"
				r.Error = err.Error()
				addManagedNetworkHelp(c, &r, task, err)
				return outputManaged(c, s, task, r)
			}
			if state != "ready" {
				r.State = state
				r.Action = "Ask the user: ‘Allow Doomer to stay up to date automatically?’ Present choices in this order: ‘Allow automatic updates (recommended)’, ‘Not now’. Explain that this also allows reporting of version/task-state metadata to " + displayManagedService(task.API) + ". Repository content requires separate upload consent. Only after approval, resume with --allow-updates."
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
		if err := managedApproval(c, app, s, &task); err != nil {
			return outputManagedNetworkError(c, s, task, err)
		}
		return nil
	}
	c.Flags().StringVar(&repo, "repo", ".", "checkout being edited")
	defaultAgent := "codex"
	if os.Getenv("CLAUDECODE") != "" {
		defaultAgent = "claude"
	}
	c.Flags().StringVar(&agent, "agent", defaultAgent, "active agent: codex or claude")
	c.Flags().StringVar(&handle, "task", "", "resume an existing task handle")
	c.Flags().StringVar(&session, "session", "", "actual harness session ID for optional hook binding")
	c.Flags().StringVar(&prompt, "prompt", "", "original user task (stored locally until upload is approved)")
	c.Flags().StringVar(&baseRef, "base", "HEAD", "starting commit for a new task when local edits began before preparation")
	c.Flags().BoolVar(&allowUpdates, "allow-updates", false, "record the user's explicit authorization for this bootstrap's update policy")
	c.Flags().BoolVar(&portable, "portable", false, "request browser approval to reuse setup across environments through private Git metadata")
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
	authorized, err := authorizeManagedUpdates(ctx, s, *task, allow, saved)
	if err != nil {
		return "", err
	}
	if authorized && !saved.Authorized {
		saved.Authorized = authorized
		if err := watch.Write(file, saved); err != nil {
			return "", err
		}
	}
	if !authorized {
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

var errManagedLoginRequired = errors.New("account login required")

func managedIdentity(ctx context.Context, app *application.App, t managed.Task) (adversarylabs.Auth, string, error) {
	auth, ok, err := scopedAuth(app.Dependencies().Auth, t.API, t.Profile, app.Dependencies().RegistryHost)
	if err != nil {
		return auth, "", err
	}
	if !ok || auth.Token == "" {
		return auth, "", errManagedLoginRequired
	}
	if adversarylabs.IsAnonymousToken(auth.Token) {
		return auth, "anonymous:" + watch.Key(auth.Token), nil
	}
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, t.API)
	account, err := client.Whoami(ctx, auth.Token)
	if err != nil {
		if errors.Is(err, adversarylabs.ErrNotAuthenticated) {
			return auth, "", errManagedLoginRequired
		}
		return auth, "", managed.NetworkError("account_verification", err)
	}
	if account.ID == "" {
		return auth, "", fmt.Errorf("account identity missing")
	}
	return auth, account.ID, nil
}

type repositoryConsent struct {
	Account, Repository, API, Profile string
	Agent                             string            `json:"agent,omitempty"`
	AllRepositories                   bool              `json:"all_repositories,omitempty"`
	RevocationID                      string            `json:"revocation_id,omitempty"`
	IntegrationRevocations            map[string]string `json:"integration_revocations,omitempty"`
	Approved                          bool
	Source                            string `json:"source,omitempty"`
	At                                time.Time
}

func managedConsent(s managed.Store, t managed.Task, token string, allow bool) (bool, error) {
	// Reuse integration-wide or legacy repository approval for the verified
	// account. A refreshed credential for the same account can resume.
	if allow {
		return true, recordManagedConsent(s, t, "explicit_cli_approval")
	}
	evidence, err := savedManagedConsentEvidence(s, t)
	return evidence != nil, err
}

func newManagedTaskCommand(app *application.App, api, profile *string) *cobra.Command {
	parent := &cobra.Command{Use: "task", Short: "Resume a durable managed review without a persistent process"}
	for _, operation := range []string{"submit", "status", "respond", "finish", "consent", "revoke", "login", "hook", "pause", "resume", "rollback", "retry", "repair", "context", "codex-policy"} {
		operation := operation
		var allow bool
		var allRepositories bool
		c := &cobra.Command{Use: operation + " HANDLE", Args: cobra.ExactArgs(1)}
		c.Flags().BoolVar(&allow, "allow", false, "record an explicit user approval; never supply on the user's behalf")
		if operation == "consent" || operation == "revoke" {
			c.Flags().BoolVar(&allRepositories, "all-repositories", false, "apply to all repositories for this agent, account, service, and profile")
		}
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
				t.Purpose = "coding"
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
				r := managedResult{State: "authentication_required", Task: t.Handle, Error: err.Error()}
				addManagedNetworkHelp(c, &r, t, err)
				return outputManaged(c, s, t, r)
			}
			if t.AccountID != "" && t.AccountID != account {
				return fmt.Errorf("task account changed; original account login required")
			}
			t.AccountID = account
			if operation == "consent" {
				if !allow {
					return fmt.Errorf("--allow requires the user's explicit approval of repository snapshots, task context and finding response uploads to %s", t.API)
				}
				if allRepositories {
					err = recordIntegrationConsent(s, t, "explicit_integration_approval", true)
				} else {
					_, err = managedConsent(s, t, auth.Token, true)
				}
				if err != nil {
					return err
				}
				if t.Portable && !t.PortableApproved {
					// Upload consent alone never grants credential persistence.
					t.PortableDeclined = true
				}
				if err = s.Save(&t); err != nil {
					return err
				}
				return outputManaged(c, s, t, managedResult{State: "consent_recorded", Task: t.Handle, Authenticated: true, UploadApproved: true})
			}
			if operation == "revoke" {
				evidence, err := savedManagedConsentEvidence(s, t)
				if err != nil {
					return err
				}
				if allRepositories || (evidence != nil && evidence.AllRepositories) {
					err = recordIntegrationConsent(s, t, "integration_revoked", false)
				} else {
					err = watch.Write(managedConsentPath(s, t), repositoryConsent{Account: t.AccountID, At: time.Now().UTC()})
				}
				if err != nil {
					return err
				}
				result := managedResult{State: "consent_revoked", Task: t.Handle, Authenticated: true}
				if t.PortableApproved {
					if err := removePortable(c.Context(), t); err != nil {
						result.Error = err.Error()
						result.Action = "Local upload approval is revoked. The portable record could not be removed; remove this repository's private portable record before using another environment."
					}
				}
				return outputManaged(c, s, t, result)
			}
			if t.Portable && !t.PortableDeclined {
				if err := checkPortable(c.Context(), t); err != nil {
					return outputManaged(c, s, t, managedResult{State: "consent_required", Authenticated: true, Error: err.Error()})
				}
			}
			approved, err := managedConsent(s, t, auth.Token, false)
			if err != nil {
				return err
			}
			if !approved {
				return outputManaged(c, s, t, managedResult{State: "consent_required", Task: t.Handle, Authenticated: true})
			}
			if operation == "codex-policy" {
				if t.Agent != "codex" {
					return fmt.Errorf("reviewer policy requires a Codex task")
				}
				if allow {
					evidence, err := savedManagedConsentEvidence(s, t)
					if err != nil {
						return err
					}
					if evidence == nil {
						return fmt.Errorf("current upload consent required")
					}
					if err := installCodexReviewPolicy(*evidence); err != nil {
						return err
					}
				}
				return outputManaged(c, s, t, managedResult{State: "codex_policy_checked", Authenticated: true, UploadApproved: true, Action: "Policy file validation does not verify activation. Reload Codex or start a fresh chat, then verify the reviewer receives the policy; managed organization policy may override it."})
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
			if operation != "hook" {
				t.LastError = ""
			}
			if err != nil {
				t.LastError = err.Error()
			}
			if saveErr := s.Save(&t); saveErr != nil {
				return saveErr
			}
			r := resultFor(t)
			r.Authenticated = true
			r.UploadApproved = true
			if err != nil {
				r.State = "operation_failed"
				r.Error = err.Error()
				addManagedNetworkHelp(c, &r, t, err)
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
	var event watch.HookInput
	if err := json.NewDecoder(io.LimitReader(c.InOrStdin(), 1<<20)).Decode(&event); err != nil {
		return err
	}
	root, err := watch.Root(c.Context(), event.CWD)
	if err != nil || root != t.Repo {
		return fmt.Errorf("hook checkout does not match task")
	}
	if t.Capabilities.SessionID != "" && event.Session != t.Capabilities.SessionID {
		return fmt.Errorf("hook session no longer owns this task")
	}
	if event.Event == "UserPromptSubmit" && t.Submission == nil && strings.TrimSpace(t.Prompt) == "" && strings.TrimSpace(event.Prompt) != "" {
		t.Prompt = event.Prompt
	}
	t.HookActivated = true
	if err := s.Save(t); err != nil {
		return err
	}
	if t.LastError != "" && (event.Event == "PostToolUse" || event.Event == "Stop") {
		return errors.New(t.LastError)
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
		if t.Stage == "review_pending" || t.Stage == "responses_required" || t.Stage == "review_failed" {
			return nil
		}
		return e.Finish(c.Context(), t, token)
	case "UserPromptSubmit", "PreToolUse":
		return nil
	default:
		return fmt.Errorf("unsupported hook event")
	}
}

func managedLogin(c *cobra.Command, app *application.App, s managed.Store, t managed.Task) error {
	if err := managedApproval(c, app, s, &t); err != nil {
		return outputManagedNetworkError(c, s, t, err)
	}
	return nil
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
	// The explicit local override also applies to tasks created before the
	// development build was installed. Prepare saves the selection under its lock.
	if developmentRuntime(task.API, task.Profile) {
		return false, nil
	}
	if task.Release == "" {
		return false, nil
	}
	binary, err := managedTaskBinary(task)
	if err != nil {
		return true, err
	}
	current, err := os.Executable()
	if err != nil {
		return true, err
	}
	if current == binary {
		return false, nil
	}
	home, err := taskRuntimeHome(task)
	if err != nil {
		return true, err
	}
	status, err := (selfupdate.Updater{Home: home, Versioned: true}).Check(task.Release)
	if err != nil || status.State != "ready" {
		return true, fmt.Errorf("task release %s is unavailable or modified", task.Release)
	}
	child := exec.CommandContext(ctx, binary, args...)
	child.Stdin = in
	child.Stdout = out
	child.Stderr = stderr
	if command.Name() != "prepare" {
		return true, child.Run()
	}
	// The newest launcher can offer wrapper maintenance even when a delayed or
	// pinned task resumes an older runtime that predates bootstrap inspection.
	var output bytes.Buffer
	child.Stdout = &output
	err = child.Run()
	var result map[string]json.RawMessage
	if err == nil && json.Unmarshal(output.Bytes(), &result) == nil && result != nil {
		updates := managedBootstrapUpdates(task)
		if len(updates) > 0 {
			result["bootstrap_updates"], err = json.Marshal(updates)
			if err != nil {
				return true, err
			}
			return true, json.NewEncoder(out).Encode(result)
		}
	}
	_, writeErr := out.Write(output.Bytes())
	if err != nil {
		return true, err
	}
	return true, writeErr
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
	if t.Release == "" || t.Release == "dev" || t.AccountID == "" {
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
