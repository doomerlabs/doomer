package cmd

import (
	"strings"
	"testing"
)

func TestManagedResponseInputExplainsSchemaAndRejectsMalformedFeedback(t *testing.T) {
	valid := `[{"lead_id":"lead","status":"dismissed","agrees":false,"reason":"verified outside scope"}]`
	items, err := decodeManagedResponses(strings.NewReader(valid))
	if err != nil || len(items) != 1 || items[0].LeadID != "lead" {
		t.Fatal(items, err)
	}
	for _, input := range []string{`{"lead_id":"lead"}`, `[{"id":"lead","response":"fixed"}]`, valid + ` {}`, `not json`} {
		items, err := decodeManagedResponses(strings.NewReader(input))
		if err == nil || items != nil || !strings.Contains(err.Error(), `"lead_id"`) || !strings.Contains(err.Error(), `"status"`) {
			t.Fatal(input, items, err)
		}
	}
}
