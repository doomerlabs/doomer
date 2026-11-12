package livewatch

import (
	"bytes"
	"context"
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
	fail := func(err error) Result { result.Error = err.Error(); return result }
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
	repository := capture.Source.Repository
	if repository == "" {
		repository = sourcesnapshot.LocalRepositoryIdentifier(repo)
	}
	repoID, baseID, err := client.UploadWorkspaceTree(ctx, cfg.Token, repository, manifest.Base, manifest.Blobs)
	if err != nil {
		return fail(err)
	}
	headRepo, headID, err := client.UploadWorkspaceTree(ctx, cfg.Token, repository, manifest.Target, manifest.Blobs)
	if err != nil {
		return fail(err)
	}
	if headRepo != repoID {
		return fail(fmt.Errorf("base and current repository do not match"))
	}
	look, deep := true, false
	group := Key(cfg.ID + "\x00" + repo)
	current, err := client.StartWorkspace(ctx, cfg.Token, adversarylabs.WorkspaceSubmission{
		GitIdentity: capture.GitIdentity, LookCloser: &look, FullReview: &deep, ReviewScope: "diff", AgentDiff: diff, Diff: diff,
		UserRequest:  cfg.Request,
		RepositoryID: repoID, BaseSnapshotID: baseID, SnapshotID: headID, Repository: repository, Branch: capture.Source.Branch,
		GroupID: group, ReviewID: Key(fmt.Sprintf("%s\x00%d", group, revision)), Revision: revision, Continuity: true, Client: map[string]string{"name": "Doomer CLI", "agent": cfg.Agent},
	})
	if err != nil {
		return fail(err)
	}
	for {
		result.ID, result.URL = current.ID, adversarylabs.WorkspaceLink(cfg.API, current)
		if current.Early != nil && (current.Early.Status == "ready" || current.Early.Status == "unavailable") {
			result.Status, result.Leads = current.Early.Status, current.Early.Leads
			if result.Status == "unavailable" {
				result.Error = current.Early.FailureCode
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
