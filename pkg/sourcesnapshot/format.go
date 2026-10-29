// Package sourcesnapshot defines the bounded, immutable CLI source transport.
package sourcesnapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxBytes = 16 << 20
const MaxFileBytes = 2 << 20
const MaxFiles = 20000

// Reference-based workspace uploads send blobs independently. These limits are
// local resource safety ceilings, separate from legacy embedded upload limits.
const MaxWorkspaceBytes = 1 << 30
const MaxWorkspaceFileBytes = 20 << 20
const MaxEncodedWorkspaceBytes = 1536 << 20

type snapshotLimits struct{ content, file, encoded int }

func limitsForWorkspace(workspace bool) snapshotLimits {
	if workspace {
		return snapshotLimits{MaxWorkspaceBytes, MaxWorkspaceFileBytes, MaxEncodedWorkspaceBytes}
	}
	return snapshotLimits{MaxBytes, MaxFileBytes, MaxBytes}
}

type Entry struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Mode string `json:"mode"`
}
type Manifest struct {
	Version int               `json:"version"`
	Scope   string            `json:"scope"`
	BaseRef string            `json:"baseRef"`
	HeadRef string            `json:"headRef"`
	Base    []Entry           `json:"base"`
	Target  []Entry           `json:"target"`
	Blobs   map[string][]byte `json:"blobs"`
}

func Digest(data []byte) string                     { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func Decode(data []byte) (Manifest, error)          { return decode(data, false) }
func DecodeWorkspace(data []byte) (Manifest, error) { return decode(data, true) }
func decode(data []byte, workspace bool) (Manifest, error) {
	var m Manifest
	limits := limitsForWorkspace(workspace)
	if len(data) > limits.encoded {
		return m, fmt.Errorf("encoded snapshot exceeds %d MiB", limits.encoded>>20)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid snapshot JSON")
	}
	// Validating the entire document also rejects trailing JSON.
	if !json.Valid(data) {
		return m, fmt.Errorf("invalid snapshot JSON")
	}
	if err := m.validate(workspace); err != nil {
		return m, err
	}
	return m, nil
}
func (m Manifest) Validate() error { return m.validate(false) }
func (m Manifest) validate(workspace bool) error {
	limits := limitsForWorkspace(workspace)
	if m.Version != 1 || (m.Scope != "worktree" && m.Scope != "staged" && m.Scope != "branch") {
		return fmt.Errorf("unsupported snapshot version or scope")
	}
	if len(m.BaseRef) > 256 || len(m.HeadRef) > 256 || m.Base == nil || m.Target == nil || m.Blobs == nil || len(m.Blobs) > MaxFiles*2 {
		return fmt.Errorf("invalid snapshot manifest")
	}
	used := map[string]bool{}
	total := 0
	for _, tree := range [][]Entry{m.Base, m.Target} {
		if len(tree) > MaxFiles {
			return fmt.Errorf("snapshot exceeds file limit")
		}
		last := ""
		for _, e := range tree {
			if !ValidPath(e.Path) || e.Path <= last {
				return fmt.Errorf("invalid or unsorted snapshot path: %s", e.Path)
			}
			last = e.Path
			if e.Mode != "100644" && e.Mode != "100755" {
				return fmt.Errorf("unsupported file type: %s", e.Path)
			}
			data, ok := m.Blobs[e.Hash]
			if !ok || len(e.Hash) != 64 || Digest(data) != e.Hash || len(data) > limits.file {
				return fmt.Errorf("invalid or oversized file: %s", e.Path)
			}
			if bytes.HasPrefix(data, []byte("version https://git-lfs.github.com/spec/v1\n")) {
				return fmt.Errorf("LFS content unavailable: %s", e.Path)
			}
			if !used[e.Hash] {
				used[e.Hash] = true
				total += len(data)
			}
		}
		// A file must not also be a parent directory of another entry.
		paths := map[string]bool{}
		for _, e := range tree {
			paths[e.Path] = true
		}
		for _, e := range tree {
			for p := path.Dir(e.Path); p != "."; p = path.Dir(p) {
				if paths[p] {
					return fmt.Errorf("conflicting snapshot paths")
				}
			}
		}
	}
	if total > limits.content || len(used) != len(m.Blobs) {
		return fmt.Errorf("snapshot contains oversized or unused blobs")
	}
	if slices.Equal(m.Base, m.Target) {
		return fmt.Errorf("snapshot contains no changes")
	}
	return nil
}
func ValidPath(p string) bool {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || strings.EqualFold(part, ".git") || strings.Contains(part, ":") {
			return false
		}
	}
	return true
}
