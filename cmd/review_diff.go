package cmd

import (
	"fmt"
	"strconv"
	"strings"
)

type reviewDiffStats struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Hunks     int `json:"hunks"`
}

type reviewFileDiff struct {
	Path                        string
	Additions, Deletions, Hunks int
	Binary                      bool
}

// Count the exact patch submitted, including files changed only by mode or
// binary contents. File headers are not added/deleted source lines.
func summarizeReviewDiff(diff string) reviewDiffStats {
	var stats reviewDiffStats
	for _, file := range reviewDiffFiles(diff) {
		stats.Files++
		stats.Additions += file.Additions
		stats.Deletions += file.Deletions
		stats.Hunks += file.Hunks
	}
	return stats
}

func reviewDiffFiles(diff string) []reviewFileDiff {
	var files []reviewFileDiff
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			header := strings.TrimPrefix(line, "diff --git ")
			path := header
			// Mode-only changes have no ---/+++ headers. Recover their shared
			// name without splitting a filename that contains spaces.
			for i := 0; i < len(header); i++ {
				if header[i] != ' ' {
					continue
				}
				left, right := diffFilePath(header[:i]), diffFilePath(header[i+1:])
				if left == right {
					path = right
					break
				}
			}
			files = append(files, reviewFileDiff{Path: path})
			inHunk = false
			continue
		}
		if len(files) == 0 {
			continue
		}
		file := &files[len(files)-1]
		switch {
		case !inHunk && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")):
			path := diffFilePath(line[4:])
			if path != "/dev/null" {
				file.Path = path
			}
		case strings.HasPrefix(line, "@@ "):
			file.Hunks++
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			file.Additions++
		case inHunk && strings.HasPrefix(line, "-"):
			file.Deletions++
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
			file.Binary = true
		}
	}
	return files
}

func diffFilePath(raw string) string {
	raw = strings.TrimSuffix(raw, "\t")
	if decoded, err := strconv.Unquote(raw); err == nil {
		raw = decoded
	}
	if strings.HasPrefix(raw, "a/") || strings.HasPrefix(raw, "b/") {
		return raw[2:]
	}
	return raw
}

func (s reviewDiffStats) String() string {
	files, hunks := "files", "hunks"
	if s.Files == 1 {
		files = "file"
	}
	if s.Hunks == 1 {
		hunks = "hunk"
	}
	return fmt.Sprintf("Diff: %d %s, +%d/-%d lines, %d %s", s.Files, files, s.Additions, s.Deletions, s.Hunks, hunks)
}
