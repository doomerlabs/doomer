package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The fixture keeps stable update policy while limiting available releases to those at or before
// the explicitly selected baseline. A Go overlay leaves the captured source
// untouched and applies the same deterministic adapter to historical sources.
func candidateReleaseOverlay(source, output, version string) (string, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	original := filepath.Join(source, "internal/managed/releases.go")
	raw, err := os.ReadFile(original)
	if err != nil {
		return "", err
	}
	const loop = "for _, r := range c.Releases {"
	if strings.Count(string(raw), loop) != 1 {
		return "", fmt.Errorf("candidate release selector cannot be pinned")
	}
	pinned := strings.Replace(string(raw), loop, loop+"\n if VersionLess("+strconv.Quote(version)+", r.Version) { continue }", 1)
	derived := filepath.Join(output, "pinned-releases.go")
	if err = os.WriteFile(derived, []byte(pinned), 0400); err != nil {
		return "", err
	}
	overlay := filepath.Join(output, "release-overlay.json")
	data, err := json.Marshal(map[string]any{"Replace": map[string]string{original: derived}})
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(overlay, data, 0400); err != nil {
		return "", err
	}
	return overlay, nil
}
