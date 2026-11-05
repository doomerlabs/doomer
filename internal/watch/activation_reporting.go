package watch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func activationRoot(root string) string {
	if filepath.Base(filepath.Dir(root)) == "runtimes" {
		return filepath.Dir(filepath.Dir(root))
	}
	return root
}

// The random installation identity survives CLI upgrades and is scoped to the profile/service.
func EnsureInstallationID(root string) (string, error) {
	var id string
	err := Locked(root, "installation-identity", func() error {
		file := filepath.Join(root, "installation-id.json")
		if err := Read(file, &id); err == nil {
			if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) {
				return os.ErrInvalid
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		id = hex.EncodeToString(b[:])
		return Write(file, id)
	})
	return id, err
}

type activationRecord struct {
	Event    adversarylabs.ActivationEvent
	Sent     bool
	Attempts int
	RetryAt  time.Time
}

var revisionLine = regexp.MustCompile(`(?m)^revision: ([a-f0-9]{40})$`)

func activationFile(root, identity, agent string) string {
	return filepath.Join(activationRoot(root), "activations", Key(identity+"\x00"+agent)+".json")
}

// Called only after a real successful tool hook, never by an activation check.
// This writes a small local outbox record; it performs no network request.
func queueActivation(ctx context.Context, root, path, agent string, cfg Config) {
	if !cfg.ActivationReporting || cfg.InstallationID == "" {
		return
	}
	identity, err := RepositoryIdentity(ctx, path)
	if err != nil {
		return
	}
	file := activationFile(root, identity, agent)
	_ = Locked(activationRoot(root), "activation-"+Key(identity+agent), func() error {
		var previous activationRecord
		if err := Read(file, &previous); err == nil && previous.Event.InstallationID == cfg.InstallationID {
			return nil
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		repository := "local/" + Key(cfg.InstallationID+"\x00"+identity)
		if raw, err := filesystemGit(ctx, path, "config", "--get", "remote.origin.url").Output(); err == nil {
			if remote := sourcesnapshot.RepositoryIdentifier(strings.TrimSpace(string(raw))); remote != "" {
				repository = remote
			}
		}
		client := "codex-terminal"
		skill := ".agents"
		if agent == "claude" {
			client = "claude-code"
			skill = ".claude"
		} else if os.Getenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE") == "Codex Desktop" {
			client = "codex-desktop"
		}
		revision := ""
		if data, err := os.ReadFile(filepath.Join(path, skill, "skills/doomer-review/SKILL.md")); err == nil {
			if match := revisionLine.FindSubmatch(data); len(match) == 2 {
				revision = string(match[1])
			}
		}
		return Write(file, activationRecord{Event: adversarylabs.ActivationEvent{
			InstallationID: cfg.InstallationID, Repository: repository, Agent: agent, Client: client,
			OS: runtime.GOOS, Architecture: runtime.GOARCH, CLIVersion: cfg.RuntimeVersion,
			SkillRevision: revision, ActivatedAt: time.Now().UTC().Format(time.RFC3339),
		}})
	})
}

type activationClient interface {
	ReportActivation(context.Context, string, adversarylabs.ActivationEvent) error
}

// Independent from the review loop. Failures are bounded and never change review state.
func (r Runner) reportActivations(ctx context.Context, path string) {
	client, ok := r.Client.(activationClient)
	if !ok || !r.Config.ActivationReporting {
		return
	}
	identity, err := RepositoryIdentity(ctx, path)
	if err != nil {
		return
	}
	for _, directory := range []string{r.Root, activationRoot(r.Root)} {
		if _, err := os.Stat(filepath.Join(directory, "paused.json")); !os.IsNotExist(err) {
			return
		}
	}
	var current Config
	if Read(filepath.Join(activationRoot(r.Root), "config.json"), &current) != nil ||
		!current.ActivationReporting || current.InstallationID != r.Config.InstallationID ||
		!current.ConsentedAt.Equal(r.Config.ConsentedAt) || !ConsentValid(current, r.Config.API, r.Config.Profile, r.Token) || !Allowed(current, path) {
		return
	}
	for _, agent := range []string{"codex", "claude"} {
		file := activationFile(r.Root, identity, agent)
		var record activationRecord
		send := false
		// Never hold the hook's outbox lock over a network call.
		_ = Locked(activationRoot(r.Root), "activation-"+Key(identity+agent), func() error {
			if Read(file, &record) != nil || record.Sent || record.Attempts >= 3 || time.Now().Before(record.RetryAt) ||
				record.Event.InstallationID != r.Config.InstallationID || RepositoryRemoved(r.Root, path) {
				return nil
			}
			record.Attempts++
			record.RetryAt = time.Now().Add(5 * time.Minute)
			if err := Write(file, record); err != nil {
				return err
			}
			send = true
			return nil
		})
		if !send {
			continue
		}
		request, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := client.ReportActivation(request, r.Token, record.Event)
		cancel()
		if err != nil {
			continue
		}
		_ = Locked(activationRoot(r.Root), "activation-"+Key(identity+agent), func() error {
			var latest activationRecord
			if Read(file, &latest) != nil || latest.Event != record.Event {
				return nil
			}
			latest.Sent = true
			return Write(file, latest)
		})
	}
}

func (r Runner) runActivationReporting(ctx context.Context, path string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		r.reportActivations(ctx, path)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
