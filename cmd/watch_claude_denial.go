package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"mvdan.cc/sh/v3/syntax"
)

func reportClaudeReviewDenial(ctx context.Context, key string, input watching.HookInput) {
	var tool struct{ Command string }
	if json.Unmarshal(input.Input, &tool) != nil {
		return
	}
	executable, err := turnExecutable()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if deniedFindingCommand(tool.Command, executable, key) {
		_, _ = livewatch.Call(ctx, key, "finding-denied")
		return
	}
	id := deniedAssessmentRequest(tool.Command, executable, key)
	if id == "" {
		return
	}
	denial := livewatch.AssessmentDenied
	switch {
	case strings.HasPrefix(input.Reason, "[Undermining Oversight]"):
		denial = livewatch.AssessmentOversightDenied
	case strings.HasPrefix(input.Reason, "Classifier unavailable"):
		denial = livewatch.AssessmentClassifierUnavailable
	case strings.HasPrefix(input.Reason, "Auto mode could not evaluate this action"):
		denial = livewatch.AssessmentNoClassifierVerdict
	}
	_ = livewatch.RecordAssessmentDenial(ctx, key, id, denial)
}

func deniedFindingCommand(command, executable, key string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return false
	}
	call := file.Stmts[0]
	return assessmentShellCall(call, executable, "watch", "findings", "ls", "--control", key) ||
		assessmentShellCall(call, executable, "watch", "findings", "respond", "--control", key)
}

// Only attribute denials with a literal request ID in the submitted JSON. The
// denied command is never expanded, run, or included in the lifecycle event.
func deniedAssessmentRequest(command, executable, key string) string {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return ""
	}
	call := file.Stmts[0]
	if call.Negated || call.Background || call.Coprocess {
		return ""
	}
	source := call
	if pipe, ok := call.Cmd.(*syntax.BinaryCmd); ok {
		if pipe.Op != syntax.Pipe || len(call.Redirs) != 0 || !assessmentShellCall(pipe.X, "cat") {
			return ""
		}
		source, call = pipe.X, pipe.Y
		if len(call.Redirs) != 0 {
			return ""
		}
	}
	if !assessmentShellCall(call, executable, "watch", "hypotheses", "--control", key) || len(source.Redirs) != 1 {
		return ""
	}
	redirect := source.Redirs[0]
	if (redirect.Op != syntax.Hdoc && redirect.Op != syntax.DashHdoc) || (redirect.N != nil && redirect.N.Value != "0") || redirect.Hdoc == nil {
		return ""
	}
	var assessment struct {
		ID string `json:"request_id"`
	}
	if json.Unmarshal([]byte(redirect.Hdoc.Lit()), &assessment) != nil {
		return ""
	}
	return assessment.ID
}

func assessmentShellCall(stmt *syntax.Stmt, args ...string) bool {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Negated || stmt.Background || stmt.Coprocess || len(call.Assigns) != 0 || len(call.Args) != len(args) {
		return false
	}
	for i, word := range call.Args {
		value := word.Lit()
		if len(word.Parts) == 1 {
			if quoted, ok := word.Parts[0].(*syntax.SglQuoted); ok && !quoted.Dollar {
				value = quoted.Value
			}
		}
		if value != args[i] {
			return false
		}
	}
	return true
}
