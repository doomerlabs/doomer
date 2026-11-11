package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"
)

func TestStateAccessErrorPreservesOperationAndRecovery(t *testing.T) {
	t.Setenv("DOOMER_CONFIG_DIR", "")
	root := filepath.Join(t.TempDir(), "private state")
	for _, cause := range []error{syscall.EPERM, syscall.EACCES} {
		err := fmt.Errorf("initialize: %w", &os.PathError{Op: "chmod", Path: root, Err: cause})
		var out bytes.Buffer
		args := []string{"/path with spaces/doomer", "prepare", "--prompt", "literal $(touch ignored)"}
		if !writeManagedStateAccess(&out, err, root, args, "/repo", "", false) {
			t.Fatal("permission error not handled")
		}
		var result managedStateAccess
		if e := json.Unmarshal(out.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if result.State != "state_access_required" || result.Operation != "chmod" || result.Path != root || result.Task != "" || result.Error != err.Error() || !reflect.DeepEqual(result.RetryCommand, args) {
			t.Fatalf("lost recovery evidence: %+v", result)
		}
		if result.RetryCommandDisplay != "'/path with spaces/doomer' 'prepare' '--prompt' 'literal $(touch ignored)'" {
			t.Fatal(result.RetryCommandDisplay)
		}
		for _, prohibited := range []string{`"authenticated":true`, `"upload_approved":true`, `"state":"consent_required"`} {
			if strings.Contains(out.String(), prohibited) {
				t.Fatal("invented authorization result", out.String())
			}
		}
	}
}

func TestStateAccessErrorDoesNotMisclassifyOtherFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	for _, err := range []error{
		&os.PathError{Op: "open", Path: root, Err: syscall.ENOSPC},
		&os.PathError{Op: "open", Path: root + "-other/file", Err: syscall.EACCES},
		errors.New("permission denied by approval reviewer"),
	} {
		var out bytes.Buffer
		if writeManagedStateAccess(&out, err, root, nil, "", "", false) || out.Len() != 0 {
			t.Fatalf("misclassified %v: %s", err, &out)
		}
	}
}

func TestRecoveryCommandRetainsBaselineAndTaskIdentity(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			isolate(t)
			repo := newWatchTestRepo(t)
			root := newRootCommand(nil)
			root.SetContext(context.Background())
			c, _, err := root.Find([]string{"prepare"})
			if err != nil {
				t.Fatal(err)
			}
			c.SetContext(context.Background())
			args := []string{"--agent=codex", "--repo=" + repo, "--session=session", "--prompt=original task"}
			if existing {
				args = append(args, "--task=existing-handle")
			}
			if err := c.ParseFlags(args); err != nil {
				t.Fatal(err)
			}
			retry, handle, stdin := managedRetryCommand(c, "https://doomer.ai/api", "profile", nil)
			base, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
			if err != nil {
				t.Fatal(err)
			}
			gotBase := false
			for _, arg := range retry {
				if arg == "--base="+strings.TrimSpace(string(base)) {
					gotBase = true
				}
			}
			if gotBase == existing || stdin || existing && handle != "existing-handle" || !existing && handle != "" {
				t.Fatalf("wrong identity or baseline: %q %q %v", retry, handle, stdin)
			}
		})
	}
}

func TestStateAccessWrapperPreservesFailureAndStdinRequirement(t *testing.T) {
	isolate(t)
	s, _ := managedStoreLocation()
	want := &os.PathError{Op: "open", Path: filepath.Join(s.Root, "task.json"), Err: syscall.EACCES}
	root := newRootCommand(nil)
	c, _, _ := root.Find([]string{"task", "respond", "handle"})
	c.RunE = func(*cobra.Command, []string) error { return want }
	api, profile := "https://doomer.ai/api", "default"
	wrapManagedStateRecovery(root, &api, &profile)
	var out bytes.Buffer
	c.SetOut(&out)
	if err := c.RunE(c, []string{"handle"}); !errors.Is(err, syscall.EACCES) {
		t.Fatal(err)
	}
	var result managedStateAccess
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err, out.String())
	}
	if result.RetryEnv["DOOMER_CONFIG_DIR"] != os.Getenv("DOOMER_CONFIG_DIR") {
		t.Fatal("lost state directory", result)
	}
	if result.Task != "handle" || !result.RequiresOriginalStdin {
		t.Fatal(result)
	}
	if _, err := os.Stat(s.Root); !os.IsNotExist(err) {
		t.Fatal("reporting initialized state", err)
	}
}

// The launcher dispatches existing tasks before Cobra runs their handlers.
func TestDispatchStateAccessReportsOriginalTask(t *testing.T) {
	root := t.TempDir()
	want := &os.PathError{Op: "open", Path: root, Err: syscall.EPERM}
	var out bytes.Buffer
	args := []string{"task", "status", "original-handle"}
	if err := managedDispatchStateError(&out, want, root, args, "original-handle"); !errors.Is(err, syscall.EPERM) {
		t.Fatal(err)
	}
	var result managedStateAccess
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Task != "original-handle" || !reflect.DeepEqual(result.RetryCommand[1:], args) {
		t.Fatal(result)
	}
}

func TestPreparationRecoveryResumesPersistedHandle(t *testing.T) {
	isolate(t)
	s, _ := managedStoreLocation()
	root := newRootCommand(nil)
	c, _, _ := root.Find([]string{"prepare"})
	c.SetContext(context.Background())
	c.RunE = func(*cobra.Command, []string) error {
		return &managedTaskFailure{handle: "saved-handle", err: &os.PathError{Op: "mkdir", Path: filepath.Join(s.Root, "sessions"), Err: syscall.EPERM}}
	}
	api, profile := "https://doomer.ai/api", "default"
	wrapManagedStateRecovery(root, &api, &profile)
	if err := c.ParseFlags([]string{"--prompt=original task", "--session=original-session", "--allow-updates", "--portable"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c.SetOut(&out)
	if err := c.RunE(c, nil); !errors.Is(err, syscall.EPERM) {
		t.Fatal(err)
	}
	var result managedStateAccess
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	want := []string{"--api-url", api, "--profile", profile, "prepare", "--task", "saved-handle", "--session", "original-session", "--allow-updates=true", "--portable=true"}
	if result.Task != "saved-handle" || !reflect.DeepEqual(result.RetryCommand[1:], want) {
		t.Fatal(result)
	}
}

func TestHookStateFailureIsActionableWithoutStopLoop(t *testing.T) {
	var out bytes.Buffer
	err := &os.PathError{Op: "mkdir", Path: "/private-state/tasks", Err: syscall.EPERM}
	writeManagedStateAccess(&out, err, "/private-state", []string{"doomer", "task", "status", "handle"}, "/repo", "handle", false)
	feedback := managedHookFeedback("PostToolUse", out.Bytes(), errors.New("exit status 1"))
	raw, _ := json.Marshal(feedback)
	if !strings.Contains(string(raw), "Recovery command") || !strings.Contains(string(raw), "actual reason") {
		t.Fatal(string(raw))
	}
	if feedback := managedHookFeedback("Stop", out.Bytes(), errors.New("exit status 1")); len(feedback) != 0 {
		t.Fatal(feedback)
	}
}

func TestRecoveryDoesNotRecomputeBaselineAfterFailure(t *testing.T) {
	isolate(t)
	s, _ := managedStoreLocation()
	repo := newWatchTestRepo(t)
	before := reviewGit(t, repo, "rev-parse", "HEAD")
	root := newRootCommand(nil)
	c, _, _ := root.Find([]string{"prepare"})
	c.SetContext(context.Background())
	c.RunE = func(*cobra.Command, []string) error {
		reviewGit(t, repo, "commit", "--allow-empty", "-m", "concurrent commit")
		return &os.PathError{Op: "mkdir", Path: filepath.Join(s.Root, "tasks"), Err: syscall.EPERM}
	}
	api, profile := "https://doomer.ai/api", "default"
	wrapManagedStateRecovery(root, &api, &profile)
	if err := c.ParseFlags([]string{"--repo=" + repo, "--prompt=original task", "--base=HEAD"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c.SetOut(&out)
	if err := c.RunE(c, nil); !errors.Is(err, syscall.EPERM) {
		t.Fatal(err)
	}
	var result managedStateAccess
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.RetryCommandDisplay, "--base="+before) || reviewGit(t, repo, "rev-parse", "HEAD") == before {
		t.Fatal("recovery lost the original commit", result)
	}
}
