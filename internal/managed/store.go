package managed

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

var handlePattern = regexp.MustCompile(`^[a-f0-9]{48}$`)

type Capabilities struct {
	Hooks               bool   `json:"hooks"`
	PersistentProcesses bool   `json:"persistent_processes"`
	DurableStorage      bool   `json:"durable_storage"`
	BrowserCallback     bool   `json:"browser_callback"`
	SessionID           string `json:"session_id,omitempty"`
}

type Task struct {
	RuntimeHome    string                             `json:"runtime_home,omitempty"`
	Schema         int                                `json:"schema"`
	Handle         string                             `json:"handle"`
	Repo           string                             `json:"repo"`
	RepositoryID   string                             `json:"repository_id"`
	API            string                             `json:"api"`
	Profile        string                             `json:"profile"`
	Agent          string                             `json:"agent"`
	OfferedRelease string                             `json:"offered_release,omitempty"`
	Release        string                             `json:"release"`
	Bootstrap      Bootstrap                          `json:"bootstrap"`
	Capabilities   Capabilities                       `json:"capabilities"`
	Base           string                             `json:"base"`
	Prompt         string                             `json:"prompt"`
	AccountID      string                             `json:"account_id,omitempty"`
	Created        time.Time                          `json:"created"`
	Updated        time.Time                          `json:"updated"`
	Stage          string                             `json:"stage"`
	Revision       int                                `json:"revision"`
	Submission     *adversarylabs.WorkspaceSubmission `json:"submission,omitempty"`
	Review         *adversarylabs.WorkspaceReview     `json:"review,omitempty"`
	ReviewedTree   string                             `json:"reviewed_tree,omitempty"`
	Responses      map[string]Response                `json:"responses,omitempty"`
	Paused         bool                               `json:"paused"`
	LastError      string                             `json:"last_error,omitempty"`
	HookActivated  bool                               `json:"hook_activated"`
}

type Response struct {
	LeadID string `json:"lead_id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	Agrees *bool  `json:"agrees"`
	Sent   bool   `json:"sent"`
}

type Store struct{ Root string }

func NewHandle() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s Store) path(handle string) (string, error) {
	if !handlePattern.MatchString(handle) {
		return "", fmt.Errorf("invalid task handle")
	}
	return filepath.Join(s.Root, "tasks", handle+".json"), nil
}
func (s Store) Load(handle string) (Task, error) {
	var task Task
	path, err := s.path(handle)
	if err != nil {
		return task, err
	}
	if err = watch.Read(path, &task); err != nil {
		return task, err
	}
	if task.Schema != Protocol || task.Handle != handle {
		return task, fmt.Errorf("invalid task record")
	}
	return task, nil
}
func (s Store) Save(task *Task) error {
	path, err := s.path(task.Handle)
	if err != nil {
		return err
	}
	task.Updated = time.Now().UTC()
	return watch.Write(path, task)
}
func (s Store) Lock(handle string) (*publock.Lock, error) {
	if _, err := s.path(handle); err != nil {
		return nil, err
	}
	return publock.TryAcquire(s.Root, "task-"+handle)
}
func (s Store) Observe(task Task, state string) error {
	// Local inventory is authoritative even when the reporting service is offline.
	return watch.Write(filepath.Join(s.Root, "observations", task.Handle+".json"), map[string]any{
		"task": task.Handle, "release": task.Release, "bootstrap_protocol": task.Bootstrap.Policy.Protocol,
		"policy": task.Bootstrap.Policy, "state": state, "observed_at": time.Now().UTC(),
		"agent": task.Agent, "hook_activated": task.HookActivated, "paused": task.Paused,
	})
}
func (s Store) Init() error {
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(s.Root)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed state must be a private directory")
	}
	return os.Chmod(s.Root, 0700)
}
