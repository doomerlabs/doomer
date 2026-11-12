// Package turnwatch owns one transient, in-memory review worker per agent turn.
// It does not use the legacy watcher's on-disk runtime or upload caches.
package turnwatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func Key(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }

type Identity struct {
	Namespace string `json:"namespace"`
	Agent     string `json:"agent"`
	Session   string `json:"session"`
	Turn      string `json:"turn"`
}

// A turnless agent uses a session rendezvous for its current worker. Starting
// its next prompt cancels the previous worker before binding that rendezvous.
func (id Identity) Key() string {
	return Key(id.Namespace + "\x00" + id.Agent + "\x00" + id.Session + "\x00" + id.Turn)
}

type Config struct {
	Identity     Identity      `json:"identity"`
	Directory    string        `json:"directory"`
	API          string        `json:"api"`
	Token        string        `json:"token"`
	Prompt       string        `json:"prompt"`
	RequestTurn  string        `json:"request_turn"`
	ManagerHome  string        `json:"manager_home"`
	DrainTimeout time.Duration `json:"drain_timeout,omitempty"`
}

type Client interface {
	UploadWorkspaceTree(context.Context, string, string, []sourcesnapshot.Entry, map[string][]byte) (string, string, error)
	StartWorkspace(context.Context, string, adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error)
	GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error)
}

type Result struct {
	Repository string                        `json:"repository"`
	ID         string                        `json:"id,omitempty"`
	URL        string                        `json:"url,omitempty"`
	Status     string                        `json:"status"`
	Leads      []adversarylabs.WorkspaceLead `json:"leads,omitempty"`
	Error      string                        `json:"error,omitempty"`
}

type Response struct {
	Ready     bool     `json:"ready"`
	API       string   `json:"api,omitempty"`
	Directory string   `json:"directory,omitempty"`
	Results   []Result `json:"results,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type request struct {
	Key       string `json:"key"`
	Operation string `json:"operation"`
}
