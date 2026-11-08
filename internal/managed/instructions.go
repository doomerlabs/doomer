package managed

import (
	_ "embed"
	"strings"
)

// Instructions are compiled into the same executable that implements them.
//
//go:embed instructions.md
var instructionsTemplate string

var Instructions = expandPromptRules(instructionsTemplate)

//go:embed bootstrap.md
var bootstrapTemplate string

//go:embed permission-prompt-rules.md
var PermissionPromptRules string

//go:embed upgrade-prompt-rules.md
var UpgradePromptRules string

//go:embed final-review-rules.md
var FinalReviewRules string

func expandPromptRules(text string) string {
	text = strings.ReplaceAll(text, "{{final_review_rules}}", strings.TrimSpace(FinalReviewRules))
	text = strings.ReplaceAll(text, "{{permission_prompt_rules}}", strings.TrimSpace(PermissionPromptRules))
	return strings.ReplaceAll(text, "{{upgrade_prompt_rules}}", strings.TrimSpace(UpgradePromptRules))
}
