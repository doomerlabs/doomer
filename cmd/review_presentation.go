package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"unicode"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"golang.org/x/term"
)

func reviewLine(value string) string {
	return strings.Join(strings.Fields(reviewText(value)), " ")
}

func shortReviewDigest(digest string) string {
	if len(digest) >= 7 {
		return digest[:7]
	}
	return "working tree"
}

func reviewTerminal(w io.Writer) bool {
	file, ok := w.(interface{ Fd() uintptr })
	return ok && os.Getenv("TERM") != "dumb" && term.IsTerminal(int(file.Fd()))
}

func reviewLinkLabel(link string, terminal bool) string {
	parsed, err := url.Parse(link)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || strings.IndexFunc(link, unicode.IsControl) >= 0 {
		return "Review link unavailable"
	}
	if terminal {
		// OSC 8 keeps the full capability URL clickable without displaying it.
		return "\x1b]8;;" + link + "\x1b\\Open review ↗\x1b]8;;\x1b\\"
	}
	// Preserve a usable link in redirected output, without terminal escapes.
	return "Open review: " + link
}

func workspaceReviewText(review adversarylabs.WorkspaceReview, link string, terminal bool) (string, error) {
	var out strings.Builder
	bold := func(text string) string {
		if terminal {
			return "\x1b[1m" + text + "\x1b[0m"
		}
		return text
	}
	const divider = "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"
	out.WriteString("\n" + divider)
	lookEnabled := review.LookCloserEnabled == nil || *review.LookCloserEnabled
	if lookEnabled {
		fmt.Fprintf(&out, "  %s\n\n", bold("RESULTS · LOOK CLOSER"))
		switch {
		case review.Early == nil:
			out.WriteString("  Look Closer is queued.\n")
		case review.Early.Status == "ready":
			count := len(review.Early.Leads)
			if count == 0 {
				fmt.Fprintf(&out, "  %s\n", bold("✓ No areas found that need a closer look."))
			} else {
				headline := fmt.Sprintf("→ %d areas need a closer look", count)
				if count == 1 {
					headline = "→ 1 area needs a closer look"
				}
				fmt.Fprintf(&out, "  %s\n", bold(headline))
				for i, lead := range review.Early.Leads {
					if count == 1 {
						fmt.Fprintf(&out, "\n  %s\n", reviewLine(lead.Concern))
					} else {
						fmt.Fprintf(&out, "\n  %d. %s\n", i+1, reviewLine(lead.Concern))
					}
					if lead.Path != "" {
						fmt.Fprintf(&out, "     %s", reviewLine(lead.Path))
						if lead.Line > 0 {
							fmt.Fprintf(&out, ":%d", lead.Line)
						}
						out.WriteByte('\n')
					}
				}
			}
		default:
			fmt.Fprintf(&out, "  Look Closer: %s.\n", reviewLine(review.Early.Status))
		}
	}
	if review.Full != nil && review.Full.Enabled {
		out.WriteByte('\n')
		if review.Full.Result != nil {
			fmt.Fprintf(&out, "  %s\n\n", bold("RESULTS · DEEP REVIEW"))
			result := review.Full.Result
			fmt.Fprintf(&out, "  Conclusion: %s\n  %s\n  Findings: %d\n", reviewLine(result.Conclusion), reviewLine(result.Summary), len(result.Findings))
			for _, finding := range result.Findings {
				var pretty bytes.Buffer
				if err := json.Indent(&pretty, finding, "  ", "  "); err != nil {
					return "", err
				}
				fmt.Fprintf(&out, "\n  %s\n", reviewText(pretty.String()))
			}
		} else {
			if !lookEnabled {
				fmt.Fprintf(&out, "  %s\n\n", bold("RESULTS · DEEP REVIEW"))
			}
			fmt.Fprintf(&out, "  Deep review: %s.\n", reviewLine(review.Full.Status))
		}
	} else if lookEnabled && review.Early != nil && review.Early.Status == "ready" {
		out.WriteString("\n  Consider a Deep review for a more thorough analysis.\n")
	}
	if review.Error != "" && review.Error != "full_review_disabled" && review.Error != "full_review_temporarily_disabled" {
		fmt.Fprintf(&out, "\n  %s\n", reviewLine(review.Error))
	}
	fmt.Fprintf(&out, "\n  %s\n", reviewLinkLabel(link, terminal))
	out.WriteString(divider)
	return out.String(), nil
}
