package githubreview

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/doomerlabs/doomer/pkg/review"
)

//go:embed default.md
var DefaultVoicePrompt string

const maxVoiceBytes = 32 << 10

// Voice relative paths, first hit wins within a root.
// Prefer agent/ (package agent identity), then train/, then lowercase root, then legacy VOICE.md.
var voiceRelPaths = []string{
	filepath.Join("agent", "voice.md"),
	filepath.Join("train", "voice.md"),
	"voice.md",
	"VOICE.md", // legacy
}

// LocalPackageRoots returns absolute paths for args that look like local
// adversary package directories (adversary.yaml or agent/scope.md / docs/scope.md).
func LocalPackageRoots(args []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		st, err := os.Stat(a)
		if err != nil || !st.IsDir() {
			continue
		}
		abs, err := filepath.Abs(a)
		if err != nil || seen[abs] {
			continue
		}
		if _, err := os.Stat(filepath.Join(abs, "adversary.yaml")); err != nil {
			if _, err2 := os.Stat(filepath.Join(abs, "agent", "scope.md")); err2 != nil {
				if _, err3 := os.Stat(filepath.Join(abs, "docs", "scope.md")); err3 != nil {
					continue
				}
			}
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

// ResolveVoice loads a voice prompt from the first existing candidate under roots
// (package dirs first, then review target). Falls back to the CLI-embedded default.
func ResolveVoice(roots ...string) (prompt string, info VoiceInfo) {
	info = VoiceInfo{Source: "cli_default", ExampleBank: HasVoiceExampleBank(DefaultVoicePrompt)}
	prompt = DefaultVoicePrompt
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		for _, rel := range voiceRelPaths {
			p := filepath.Join(abs, rel)
			raw, err := os.ReadFile(p)
			if err != nil || len(raw) == 0 {
				continue
			}
			if len(raw) > maxVoiceBytes || !utf8.Valid(raw) {
				continue
			}
			text := string(raw)
			return text, VoiceInfo{
				Source:      "package",
				Path:        rel,
				ExampleBank: HasVoiceExampleBank(text),
			}
		}
	}
	return prompt, info
}

// TemplateBody builds a short deterministic fallback when a voice rewrite fails.
func TemplateBody(adversary string, f review.Finding, pathStr string, line *int) string {
	lead := strings.TrimSpace(f.Title)
	if lead == "" || len(strings.Fields(lead)) <= 2 || len(strings.Fields(lead)) > 40 {
		if summary := strings.TrimSpace(f.Summary); summary != "" {
			lead = summary
		}
	}
	if words := strings.Fields(lead); len(words) > 40 {
		lead = strings.Join(words[:40], " ")
	}
	lead = sentence(lead)
	action := strings.TrimSpace(f.Recommendation)
	if before, _, ok := strings.Cut(action, ": "); ok && len(strings.Fields(action)) > 40 && len(strings.Fields(before)) <= 20 {
		action = before
	}
	if len(strings.Fields(lead))+len(strings.Fields(action)) > 50 {
		action = ""
	}
	body := lead
	if action != "" && !strings.EqualFold(strings.TrimRight(lead, ".!?"), strings.TrimRight(action, ".!?")) {
		body += " " + sentence(action)
	}
	return strings.TrimSpace(body) + "\n\n" + Marker(adversary, f.ID, pathStr, line) + "\n"
}

func sentence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text != "" && !strings.ContainsAny(text[len(text)-1:], ".!?") {
		text += "."
	}
	return text
}

// EnsureMarker appends marker if missing.
func EnsureMarker(body, adversary, findingID, pathStr string, line *int) string {
	m := Marker(adversary, findingID, pathStr, line)
	if strings.Contains(body, "adversary-review:v1") {
		return body
	}
	body = strings.TrimRight(body, "\n") + "\n\n" + m + "\n"
	return body
}

// EnsurePlannedMarker replaces any legacy marker with the provenance-rich v2
// marker for a projected finding.
func EnsurePlannedMarker(body string, comment PlannedComment) string {
	body = stripReviewMarker(body)
	return strings.TrimRight(body, "\n") + "\n\n" + MarkerV2(comment) + "\n"
}

func stripReviewMarker(body string) string {
	for {
		start := strings.Index(body, "<!-- adversary-review:v")
		if start < 0 {
			return body
		}
		endRel := strings.Index(body[start:], "-->")
		if endRel < 0 {
			return body[:start]
		}
		body = body[:start] + body[start+endRel+3:]
	}
}
