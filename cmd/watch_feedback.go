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
		result, err := livewatch.SubmitFeedback(ctx, key, feedback)
		if err != nil {
			return err
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"recorded": len(feedback), "report": makeFeedbackReport(result)})
	}
	return c
}

func newWatchInstructionsCommand() *cobra.Command {
	var key string
	c := &cobra.Command{Use: "instructions", Short: "Read finding details and response instructions for a watch", Args: cobra.NoArgs}
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.Flags().StringVar(&key, "control", "", "watch control key supplied by the hook")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if !watchControlKey.MatchString(key) {
			return fmt.Errorf("a valid --control key from the hook is required")
		}
		ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
		defer cancel()
		result, err := livewatch.Call(ctx, key, "results")
		if err != nil {
			return err
		}
		text := feedbackInstructions(result, key)
		if text == "" {
			text = feedbackReport(result)
		}
		_, err = fmt.Fprintln(c.OutOrStdout(), text)
		return err
	}
	return c
}

// report drains the current edits without stopping the watcher. The agent reads
// this result before its final answer; the Stop hook still owns shutdown.
func newWatchReportCommand() *cobra.Command {
	var key string
	c := &cobra.Command{Use: "report", Short: "Wait for reviews and read the current watch report", Args: cobra.NoArgs}
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.Flags().StringVar(&key, "control", "", "watch control key supplied by the hook")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if !watchControlKey.MatchString(key) {
			return fmt.Errorf("a valid --control key from the hook is required")
		}
		ctx, cancel := context.WithTimeout(c.Context(), 55*time.Second)
		defer cancel()
		result, err := livewatch.Call(ctx, key, "checkpoint")
		if err != nil {
			return err
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(makeFeedbackReport(result))
	}
	return c
}

func watchAgentContext(key string) map[string]any {
	executable, err := os.Executable()
	if err != nil {
		executable = "doomer"
	}
	text := fmt.Sprintf("Doomer is watching changes for this request. Before your final answer, run %s watch report --control %s to wait for reviews and read their report. If unanswered findings remain, retrieve them with %s watch instructions --control %s, investigate each, record decisions using watch respond, then run watch report again to review any resulting edits. A Stop hook saying suggestions are ready is a reminder to complete this process. Once report returns complete=true, incorporate the report into ONE final answer; no further watch commands are needed. Use natural language with links embedded in descriptive phrases such as CLI changes or changes in the named repository. Briefly describe actual fixes or unresolved deferred concerns; otherwise use one short review sentence. Do not repeat setup instructions, counts, response JSON, or the control key to the user. Do not claim success if the report contains errors.", watching.Quote(executable), key, watching.Quote(executable), key)
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "UserPromptSubmit", "additionalContext": text}}
}

func pendingFeedbackHook(result livewatch.Response) map[string]any {
	count := 0
	for _, finding := range result.Findings {
		if finding.Disposition == nil {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	label := "suggestions"
	if count == 1 {
		label = "suggestion"
	}
	return map[string]any{"decision": "block", "reason": fmt.Sprintf("Doomer has %d %s ready for review.", count, label)}
}

func feedbackInstructions(result livewatch.Response, key string) string {
	var pending []livewatch.Finding
	for _, finding := range result.Findings {
		if finding.Disposition == nil {
			pending = append(pending, finding)
		}
	}
	if len(pending) == 0 {
		return ""
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
	text.WriteString("Use status=fixed with agrees=true only for a verified fix; dismissed with agrees=false for a disproved finding; deferred for an unresolved or out-of-scope finding (agrees=null if unassessed). Give a specific reason for each. A failed submission is not recorded; resolve the error and retry.\nKeep the response JSON in your tool call. Do not dump the findings into the user's answer. After recording decisions, run watch report with the same control key before answering. It checks any resulting edits and returns the report and review links for your final answer.")
	return text.String()
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
	// Completed reviews belong in the agent's final answer, not another visible
	// hook message that forces a second answer.
	return map[string]any{}
}

type feedbackReview struct {
	Repository string `json:"repository"`
	URL        string `json:"url"`
}

type feedbackDetail struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Concern    string `json:"concern"`
	Reason     string `json:"reason"`
}

type watchFeedbackReport struct {
	Complete         bool             `json:"complete"`
	Suggestions      int              `json:"suggestions"`
	Fixed            int              `json:"fixed"`
	Disagreed        int              `json:"disagreed"`
	Deferred         int              `json:"deferred"`
	Unanswered       int              `json:"unanswered"`
	Reviews          []feedbackReview `json:"reviews"`
	Fixes            []feedbackDetail `json:"fixes"`
	DeferredFindings []feedbackDetail `json:"deferred_findings"`
	Errors           []string         `json:"errors"`
	SuggestedSummary string           `json:"suggested_summary"`
}

func makeFeedbackReport(result livewatch.Response) watchFeedbackReport {
	report := watchFeedbackReport{Suggestions: len(result.Findings), Reviews: []feedbackReview{}, Fixes: []feedbackDetail{}, DeferredFindings: []feedbackDetail{}, Errors: []string{}}
	// Prefer the latest review link for each repository over historical findings.
	links := map[string]string{}
	for _, finding := range result.Findings {
		if finding.URL != "" {
			links[finding.Repository] = finding.URL
		}
		if finding.Disposition == nil {
			report.Unanswered++
			continue
		}
		detail := feedbackDetail{Repository: finding.Repository, Path: finding.Lead.Path, Line: finding.Lead.Line, Concern: finding.Lead.Concern, Reason: finding.Disposition.Reason}
		switch finding.Disposition.Status {
		case "fixed":
			report.Fixed++
			report.Fixes = append(report.Fixes, detail)
		case "dismissed":
			report.Disagreed++
		case "deferred":
			report.Deferred++
			report.DeferredFindings = append(report.DeferredFindings, detail)
		}
	}
	for _, review := range result.Results {
		if review.URL != "" {
			links[review.Repository] = review.URL
		}
		if review.Error != "" {
			report.Errors = append(report.Errors, review.Error)
		} else if review.Status != "ready" && review.Status != "unchanged" {
			report.Errors = append(report.Errors, "Review did not complete for "+review.Repository)
		}
	}
	if result.Error != "" {
		report.Errors = append(report.Errors, result.Error)
	}
	repositories := make([]string, 0, len(links))
	for repository := range links {
		repositories = append(repositories, repository)
	}
	sort.Strings(repositories)
	var phrases []string
	escape := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]")
	for _, repository := range repositories {
		report.Reviews = append(report.Reviews, feedbackReview{Repository: repository, URL: links[repository]})
		phrases = append(phrases, "[changes in "+escape.Replace(filepath.Base(repository))+"]("+links[repository]+")")
	}
	subject := "the changes"
	if len(phrases) == 1 {
		subject = "the " + phrases[0]
	} else if len(phrases) > 1 {
		subject = "the " + strings.Join(phrases[:len(phrases)-1], ", ") + " and " + phrases[len(phrases)-1]
	}
	report.Complete = report.Unanswered == 0 && len(report.Errors) == 0
	switch {
	case len(result.Results) == 0 && len(result.Findings) == 0 && len(report.Errors) == 0:
		report.SuggestedSummary = "Doomer found no changes to review."
	case len(report.Errors) > 0:
		report.SuggestedSummary = "Doomer could not complete its review of " + subject + "."
	case report.Unanswered > 0:
		report.SuggestedSummary = "Doomer reviewed " + subject + "; some suggestions still need a decision."
	case report.Fixed > 0:
		report.SuggestedSummary = "Doomer reviewed " + subject + " and helped identify fixes."
	case report.Suggestions > 0:
		report.SuggestedSummary = "Doomer reviewed " + subject + "; we considered its suggestions and made no changes based on them."
	default:
		report.SuggestedSummary = "Doomer reviewed " + subject + " and raised no issues."
	}
	if report.Deferred > 0 {
		report.SuggestedSummary += " Some concerns remain deferred."
	}
	return report
}

func feedbackReport(result livewatch.Response) string {
	payload, _ := json.MarshalIndent(makeFeedbackReport(result), "", "  ")
	return string(payload)
}
