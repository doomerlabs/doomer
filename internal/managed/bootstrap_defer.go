package managed

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/internal/watch"
)

type bootstrapDeferral struct {
	Version string    `json:"version"`
	Until   time.Time `json:"until"`
}

func (s Store) bootstrapDeferralPath(identity string) string {
	return filepath.Join(s.Root, "bootstrap-deferrals", fmt.Sprintf("%x.json", sha256.Sum256([]byte(identity))))
}

// DeferBootstrap remembers a maintenance choice without changing repository files.
func (s Store) DeferBootstrap(identity, version string, now time.Time) error {
	return watch.Write(s.bootstrapDeferralPath(identity), bootstrapDeferral{Version: version, Until: now.Add(24 * time.Hour)})
}

func (s Store) BootstrapDeferred(identity, version string, now time.Time) bool {
	var saved bootstrapDeferral
	return watch.Read(s.bootstrapDeferralPath(identity), &saved) == nil && saved.Version == version && now.Before(saved.Until)
}
