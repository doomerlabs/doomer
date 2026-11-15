package livewatch

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

const evidenceBytes = 256 << 10

// Choose complete files from the immutable capture, never reread a changing
// checkout. Prioritize hypothesis files, then changed files, within a hard cap.
func quickEvidence(m sourcesnapshot.Manifest, hypotheses []adversarylabs.WorkspaceHypothesis) sourcesnapshot.Manifest {
	out := m
	out.Base = []sourcesnapshot.Entry{}
	out.Target = []sourcesnapshot.Entry{}
	out.Blobs = map[string][]byte{}
	chosen := map[string]bool{}
	used := 0
	old := map[string]sourcesnapshot.Entry{}
	for _, e := range m.Base {
		old[e.Path] = e
	}
	target := map[string]sourcesnapshot.Entry{}
	for _, e := range m.Target {
		target[e.Path] = e
	}
	add := func(path string) {
		if chosen[path] {
			return
		}
		a, aok := old[path]
		b, bok := target[path]
		size := 0
		for _, e := range []sourcesnapshot.Entry{a, b} {
			if e.Hash != "" {
				body := m.Blobs[e.Hash]
				if bytes.IndexByte(body, 0) >= 0 {
					return
				}
				size += len(body)
			}
		}
		if used+size > evidenceBytes {
			return
		}
		used += size
		chosen[path] = true
		if aok {
			out.Base = append(out.Base, a)
			out.Blobs[a.Hash] = m.Blobs[a.Hash]
		}
		if bok {
			out.Target = append(out.Target, b)
			out.Blobs[b.Hash] = m.Blobs[b.Hash]
		}
	}
	for _, h := range hypotheses {
		add(h.Path)
	}
	for _, e := range m.Target {
		if a, ok := old[e.Path]; !ok || a.Hash != e.Hash {
			add(e.Path)
		}
	}
	for _, e := range m.Base {
		if _, ok := target[e.Path]; !ok {
			add(e.Path)
		}
	}
	sort.Slice(out.Base, func(i, j int) bool { return out.Base[i].Path < out.Base[j].Path })
	sort.Slice(out.Target, func(i, j int) bool { return out.Target[i].Path < out.Target[j].Path })
	return out
}

type preparationClient interface {
	PrepareWorkspace(context.Context, string, string, string, string, string) error
}

// Upload and preparation are independent of Quick and of the hook's deadline.
func (w *worker) prepareInBackground(repo string, snapshot sourcesnapshot.Capture, lifecycles ...*reviewLifecycle) {
	w.background.Add(1)
	go func() {
		defer w.background.Done()
		var lifecycle *reviewLifecycle
		if len(lifecycles) > 0 {
			lifecycle = lifecycles[0]
		}
		lifecycle.event("repository_upload", "running")
		complete := false
		defer func() {
			if complete {
				lifecycle.event("repository_upload", "completed")
			} else {
				lifecycle.event("repository_upload", "failed")
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		select {
		case w.prepareSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-w.prepareSlots }()
		if !w.allowed(ctx, repo) {
			return
		}
		m, err := sourcesnapshot.DecodeWorkspace(snapshot.Bytes)
		if err != nil {
			return
		}
		name := snapshot.Source.Repository
		if name == "" {
			name = sourcesnapshot.LocalRepositoryIdentifier(repo)
		}
		id, base, err := w.client.UploadWorkspaceTree(ctx, w.cfg.Token, name, m.Base, m.Blobs)
		if err != nil {
			return
		}
		if !w.allowed(ctx, repo) {
			return
		}
		other, head, err := w.client.UploadWorkspaceTree(ctx, w.cfg.Token, name, m.Target, m.Blobs)
		if err != nil || other != id {
			return
		}
		complete = true
		if client, ok := w.client.(preparationClient); ok && w.allowed(ctx, repo) {
			reviewID := ""
			if lifecycle != nil {
				reviewID = lifecycle.id
			}
			_ = client.PrepareWorkspace(ctx, w.cfg.Token, id, head, base, reviewID)
		}
	}()
}

func uploadEvidence(ctx context.Context, client Client, cfg Config, repository string, m sourcesnapshot.Manifest, h []adversarylabs.WorkspaceHypothesis) (string, string, string, error) {
	packet := quickEvidence(m, h)
	repo, base, err := client.UploadWorkspaceTree(ctx, cfg.Token, repository, packet.Base, packet.Blobs)
	if err != nil {
		return "", "", "", err
	}
	other, head, err := client.UploadWorkspaceTree(ctx, cfg.Token, repository, packet.Target, packet.Blobs)
	if err != nil {
		return "", "", "", err
	}
	if repo != other {
		return "", "", "", fmt.Errorf("evidence repository mismatch")
	}
	return repo, base, head, nil
}
