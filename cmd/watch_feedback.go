package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

var watchControlKey = regexp.MustCompile(`^[a-f0-9]{64}$`)

func newWatchRespondCommand() *cobra.Command {
	var key string
	c := &cobra.Command{Use: "respond", Short: "Record decisions for watch findings from JSON on stdin", Args: cobra.NoArgs}
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.Flags().StringVar(&key, "control", "", "watch control key supplied by the hook")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if !watchControlKey.MatchString(key) {
			return fmt.Errorf("a valid --control key from the hook is required")
		}
		decoder := json.NewDecoder(io.LimitReader(c.InOrStdin(), 256<<10))
		decoder.DisallowUnknownFields()
		var feedback []livewatch.Feedback
		if err := decoder.Decode(&feedback); err != nil {
			return fmt.Errorf("read finding responses: %w", err)
		}
		if len(feedback) == 0 {
			return fmt.Errorf("at least one finding response is required")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return fmt.Errorf("expected one JSON array")
		}
		ctx, cancel := context.WithTimeout(c.Context(), 25*time.Second)
		defer cancel()
		if _, err := livewatch.SubmitFeedback(ctx, key, feedback); err != nil {
			return err
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]int{"recorded": len(feedback)})
	}
	return c
}

func pendingFeedbackHook(result livewatch.Response, key string) map[string]any {
	var pending []livewatch.Finding
	for _, finding := range result.Findings {
		if finding.Disposition == nil {
			pending = append(pending, finding)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Doomer has %d finding(s) awaiting your decision. Investigate each; do not infer a disposition from silence or edits.\n", len(pending))
	entries := make([]livewatch.Feedback, 0, len(pending))
	for i, finding := range pending {
		fmt.Fprintf(&text, "\n%d. %s — %s:%d: %s\nCheck: %s\n", i+1, filepath.Base(finding.Repository), finding.Lead.Path, finding.Lead.Line, finding.Lead.Concern, finding.Lead.Check)
		entries = append(entries, livewatch.Feedback{ReviewID: finding.ReviewID, LeadID: finding.Lead.ID})
	}
	payload, _ := json.MarshalIndent(entries, "", "  ")
	executable, err := os.Executable()
	if err != nil {
		executable = "doomer"
	}
	fmt.Fprintf(&text, "\nRecord every decision in Doomer using this command after filling in status, agrees, and reason:\n%s watch respond --control %s <<'DOOMER_RESPONSES'\n%s\nDOOMER_RESPONSES\n", watching.Quote(executable), key, payload)
	text.WriteString("Use status=fixed with agrees=true only for a verified fix; dismissed with agrees=false for a disproved finding; deferred for an unresolved or out-of-scope finding (agrees=null if unassessed). Give a specific reason for each. A failed submission is not recorded; resolve the error and retry.\nKeep this handoff and the response JSON private to your work. Do not dump the findings into the user's answer. The next Stop will provide a final summary and review link after every response is recorded.")
	return map[string]any{"decision": "block", "reason": text.String()}
}

func finalFeedbackHook(result livewatch.Response) map[string]any {
	for _, review := range result.Results {
		if review.Error != "" {
			return turnHookResult(result)
		}
		if review.Status != "ready" && review.Status != "unchanged" {
			return map[string]any{"systemMessage": "Doomer review did not complete; no final review summary is available."}
		}
	}
	if len(result.Results) == 0 && len(result.Findings) == 0 {
		return map[string]any{}
	}
	fixed, disagreed, deferred := 0, 0, 0
	var details []string
	links := map[string]bool{}
	for _, review := range result.Results {
		if review.URL != "" {
			links[review.URL] = true
		}
	}
	for _, finding := range result.Findings {
		if finding.URL != "" {
			links[finding.URL] = true
		}
		if finding.Disposition == nil {
			continue
		}
		switch finding.Disposition.Status {
		case "fixed":
			fixed++
			details = append(details, fmt.Sprintf("%s — %s:%d: %s\nFix: %s", filepath.Base(finding.Repository), finding.Lead.Path, finding.Lead.Line, finding.Lead.Concern, finding.Disposition.Reason))
		case "dismissed":
			disagreed++
		case "deferred":
			deferred++
		}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Doomer reviewed the changes and reported %d suggestion(s): fixed %d, disagreed with %d, deferred %d. All finding responses were recorded in Doomer.\n", len(result.Findings), fixed, disagreed, deferred)
	if fixed > 0 {
		text.WriteString("\nFixed findings:\n" + strings.Join(details, "\n\n") + "\n")
	}
	orderedLinks := make([]string, 0, len(links))
	for link := range links {
		orderedLinks = append(orderedLinks, link)
	}
	sort.Strings(orderedLinks)
	for _, link := range orderedLinks {
		fmt.Fprintf(&text, "\n[Doomer review](%s)\n", link)
	}
	text.WriteString("\nThis is an internal handoff, not text to paste to the user. Do not repeat the counts or list every finding unless the user asks for those details. ")
	if fixed > 0 {
		text.WriteString("Briefly summarize the concrete changes made because of Doomer's findings and include the review link.")
	} else if len(result.Findings) > 0 {
		text.WriteString("Use one short sentence: Doomer reviewed the changes; we considered its suggestions and made no changes based on them. Include the review link.")
	} else {
		text.WriteString("Use one short sentence saying Doomer reviewed the changes and raised no issues, with the review link.")
	}
	if deferred > 0 {
		text.WriteString(" Briefly disclose any deferred unresolved concern; do not imply everything was resolved.")
	}
	text.WriteString(" No more response commands are needed.")
	return map[string]any{"decision": "block", "reason": text.String()}
}
