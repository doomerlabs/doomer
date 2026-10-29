// Package watch connects a foreground repository watcher to local coding-agent hooks.
package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type Config struct {
	AllRepositories bool      `json:"all_repositories,omitempty"`
	Version         int       `json:"version"`
	AccountKey      string    `json:"account_key"`
	API             string    `json:"api_url"`
	Profile         string    `json:"profile"`
	Directories     []string  `json:"directories"`
	ConsentedAt     time.Time `json:"consented_at"`
}
type Owner struct {
	Session string `json:"session"`
	Turn    string `json:"turn"`
}
type Operation struct {
	Owner        Owner
	Before       string
	Started      time.Time
	Attributable bool
}
type State struct {
	Agent               string `json:"agent,omitempty"`
	QuestionRequest     string
	HypothesesSubmitted bool
	Hypotheses          []adversarylabs.WorkspaceHypothesis
	AgentDiff           string
	RequestedAt         time.Time
	Active              map[string]Operation
	Owner               Owner
	Fingerprint         string
	Ambiguous           bool
	Prompt              string
}
type Feed struct {
	Owner            Owner
	Fingerprint      string
	ReviewID         string
	URL              string
	AssessmentStatus string
	Leads            []Lead
	Updated          time.Time
}
type Lead struct {
	ID      string
	Path    string
	Line    int
	Concern string
	Check   string
}
type Pending struct {
	Number   int
	Lead     Lead
	ReviewID string
}
type Turn struct {
	Agent         string   `json:"agent,omitempty"`
	ResponseToken string   `json:"response_token,omitempty"`
	Repositories  []string `json:"repositories,omitempty"`
	Owner         Owner
	Prompt        string
	Next          int
	Pending       []Pending
	Delivered     []string
}
type Feedback struct {
	ID       string
	ReviewID string
	Status   string
	Reason   string
	Agrees   *bool
}

func Key(s string) string { return sourcesnapshot.Digest([]byte(s)) }
func Read(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return fmt.Errorf("watch state too large")
	}
	return json.Unmarshal(b, out)
}
func Write(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".watch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// Bounded cross-process lock: a crashed hook never holds up an agent indefinitely.
func Locked(root, key string, fn func() error) error {
	lock, err := publock.TryAcquire(root, key)
	if err != nil {
		return err
	}
	defer lock.Close()
	return fn()
}
func StatePath(root, path string) string {
	return filepath.Join(root, "repositories", Key(path)+".json")
}
func FeedPath(root, path string) string    { return filepath.Join(root, "reviews", Key(path)+".json") }
func TurnPath(root, session string) string { return filepath.Join(root, "turns", Key(session)+".json") }
func Allowed(cfg Config, path string) bool {
	if cfg.AllRepositories {
		return true
	}
	for _, p := range cfg.Directories {
		if p == path {
			return true
		}
	}
	return false
}
