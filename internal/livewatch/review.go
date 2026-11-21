package livewatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/pmezard/go-difflib/difflib"
)

// Diff operates on immutable snapshots in memory, including untracked files.
// No checkout, temporary source tree, or disk-backed diff cache is created.
func Diff(ctx context.Context, m sourcesnapshot.Manifest) (string, error) {
	before, after := map[string]sourcesnapshot.Entry{}, map[string]sourcesnapshot.Entry{}
	names := map[string]bool{}
	for _, e := range m.Base {
		before[e.Path] = e
		names[e.Path] = true
	}
	for _, e := range m.Target {
		after[e.Path] = e
		names[e.Path] = true
	}
	paths := make([]string, 0, len(names))
	for p := range names {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out strings.Builder
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		a, aOK := before[p]
		b, bOK := after[p]
		if aOK && bOK && a.Hash == b.Hash && a.Mode == b.Mode {
			continue
		}
		left, right := m.Blobs[a.Hash], m.Blobs[b.Hash]
		ap, bp := "a/"+p, "b/"+p
		quote := func(s string) string {
			if strings.ContainsAny(s, "\t\n\r\"\\ ") {
				return strconv.Quote(s)
			}
			return s
		}
		fmt.Fprintf(&out, "diff --git %s %s\n", quote(ap), quote(bp))
		if !aOK {
			fmt.Fprintf(&out, "new file mode %s\n", b.Mode)
		} else if !bOK {
			fmt.Fprintf(&out, "deleted file mode %s\n", a.Mode)
		} else if a.Mode != b.Mode {
			fmt.Fprintf(&out, "old mode %s\nnew mode %s\n", a.Mode, b.Mode)
		}
		if aOK && bOK && a.Hash == b.Hash {
			continue
		}
		if bytes.IndexByte(left, 0) >= 0 || bytes.IndexByte(right, 0) >= 0 {
			fmt.Fprintf(&out, "Binary files %s and %s differ\n", quote(ap), quote(bp))
			continue
		}
		if len(left)+len(right) > 16<<20 || bytes.Count(left, []byte{'\n'})+bytes.Count(right, []byte{'\n'}) > 200000 {
			return "", fmt.Errorf("changed file exceeds in-memory diff limits")
		}
		if !aOK {
			ap = "/dev/null"
		}
		if !bOK {
			bp = "/dev/null"
		}
		diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: diffLines(left), B: diffLines(right), FromFile: quote(ap), ToFile: quote(bp), Context: 3})
		if err != nil {
			return "", err
		}
		out.WriteString(diff)
		if out.Len() > 16<<20 {
			return "", fmt.Errorf("diff exceeds 16 MiB")
		}
	}
	return out.String(), nil
}

func diffLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	lines[len(lines)-1] += "\n\\ No newline at end of file\n"
	return lines
}

func review(ctx context.Context, client Client, cfg Config, repo string, capture sourcesnapshot.Capture, revision int) Result {
	result := Result{Repository: repo, Status: "unavailable"}
	fail := func(err error) Result {
		cfg.lifecycle.failure("result_delivery", err, cfg.assessment)
		result.Error = err.Error()
		return result
	}
	manifest, err := sourcesnapshot.DecodeWorkspace(capture.Bytes)
	if err != nil {
		return fail(err)
	}
	diff, err := Diff(ctx, manifest)
	if err != nil {
		return fail(err)
	}
	if strings.TrimSpace(diff) == "" {
		result.Status = "unchanged"
		return result
	}
	latest := diff
	if cfg.RequireHypotheses {
		latest, err = editScope(ctx, manifest, cfg.editBaseline, capture.Skipped)
		if err != nil {
			return fail(err)
		}
		if strings.TrimSpace(latest) == "" {
			result.Status = "unchanged"
			return result
		}
	}
	var hypotheses []adversarylabs.WorkspaceHypothesis
	if cfg.RequireHypotheses {
		if cfg.hypotheses == nil {
			return fail(fmt.Errorf("agent hypothesis handoff unavailable"))
		}
		cfg.lifecycle.event("agent_assessment", "running")
		hypotheses, err = cfg.hypotheses(ctx, repo, latest, revision)
		if err != nil {
			result.assessmentExpired = errors.Is(err, errAgentResponseTimeout) || errors.Is(err, adversarylabs.ErrAgentAssessmentExpired)
			cfg.lifecycle.failure("agent_assessment", err, cfg.assessment)
			return fail(err)
		}
		cfg.lifecycle.event("agent_assessment", "completed")
	}
	repository := capture.Source.Repository
	if repository == "" {
		repository = sourcesnapshot.LocalRepositoryIdentifier(repo)
	}
	var repoID, baseID, headID string
	if cfg.RequireHypotheses {
		cfg.lifecycle.event("evidence_upload", "running")
		repoID, baseID, headID, err = uploadEvidence(ctx, client, cfg, repository, manifest, hypotheses)
	} else {
		repoID, baseID, err = client.UploadWorkspaceTree(ctx, cfg.Token, repository, manifest.Base, manifest.Blobs)
		if err == nil {
			var other string
			other, headID, err = client.UploadWorkspaceTree(ctx, cfg.Token, repository, manifest.Target, manifest.Blobs)
			if other != repoID && err == nil {
				err = fmt.Errorf("base and current repository do not match")
			}
		}
	}
	if err != nil {
		cfg.lifecycle.event("evidence_upload", "failed")
		return fail(err)
	}
	cfg.lifecycle.event("evidence_upload", "completed")
	cfg.lifecycle.event("submission", "running")
	scope := "diff"
	if cfg.RequireHypotheses {
		scope = "latest_edit"
	}
	look := true
	group := Key(cfg.ID + "\x00" + repo)
	var handoff map[string]any
	agentDiff := latest
	if cfg.RequireHypotheses {
		handoff = map[string]any{"request_id": Key(cfg.ID + "\x00" + repo + "\x00" + fmt.Sprint(revision) + "\x00" + latest), "snapshot_id": capture.TargetTreeHash, "diff_hash": Key(latest), "watch_revision": revision, "status": "assessed", "hypothesis_count": len(hypotheses)}
		if cfg.assessment != nil {
			handoff["assessments"] = cfg.assessment.Assessments
		}
		if cfg.assessment != nil && cfg.assessment.triageReference {
			agentDiff = ""
		}
	}
	current, err := client.StartWorkspace(ctx, cfg.Token, adversarylabs.WorkspaceSubmission{AgentHandoff: handoff,
		GitIdentity: capture.GitIdentity, LookCloser: &look, ReviewScope: scope, AgentDiff: agentDiff, Diff: diff,
		UserRequest: cfg.Request, Hypotheses: hypotheses, AgentAssessmentComplete: cfg.RequireHypotheses, EvidenceContext: cfg.RequireHypotheses, FullSnapshotID: adversarylabs.WorkspaceSnapshotID(repository, manifest.Target, manifest.Blobs), FullBaseSnapshotID: adversarylabs.WorkspaceSnapshotID(repository, manifest.Base, manifest.Blobs),
		RepositoryID: repoID, BaseSnapshotID: baseID, SnapshotID: headID, Repository: repository, Branch: capture.Source.Branch,
		GroupID: group, ReviewID: reviewIdentity(cfg, repo, revision), Revision: revision, Continuity: true, Client: map[string]string{"name": "Doomer CLI", "agent": cfg.Agent},
	})
	if err != nil {
		result.assessmentExpired = errors.Is(err, adversarylabs.ErrAgentAssessmentExpired)
		cfg.lifecycle.failure("submission", err, cfg.assessment)
		return fail(err)
	}
	cfg.lifecycle.event("submission", "completed")
	for {
		result.ID = current.ID
		if link := adversarylabs.WorkspaceLink(cfg.API, current); link != "" {
			result.URL = link
		}
		if current.Early != nil && (current.Early.Status == "ready" || current.Early.Status == "unavailable") {
			result.Status, result.Leads = current.Early.Status, current.Early.Leads
			if result.Status == "unavailable" {
				result.Error = current.Early.FailureCode
			}
			if result.Status == "ready" {
				cfg.lifecycle.event("result_delivery", "completed")
			} else {
				cfg.lifecycle.failure("result_delivery", fmt.Errorf("Quick assessment unavailable: %s", current.Early.FailureCode), cfg.assessment)
			}
			return result
		}
		if current.Status == "failed" || current.Status == "cancelled" || current.Status == "completed" {
			return fail(fmt.Errorf("review ended without a Quick result: %s", current.Status))
		}
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(time.Second):
		}
		current, err = client.GetWorkspace(ctx, cfg.Token, current.ID)
		if err != nil {
			return fail(err)
		}
	}
}
