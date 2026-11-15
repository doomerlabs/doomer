package livewatch

import (
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"testing"
)

func TestUnassessedSnapshotRequiresReason(t *testing.T) {
	for _, status := range []string{"superseded", "incomplete"} {
		pending := &hypothesisWait{reply: make(chan HypothesisResponse, 1)}
		w := &worker{hypotheses: map[string]*hypothesisWait{"request": pending}}
		response := HypothesisResponse{RequestID: "request", Status: status, Hypotheses: []adversarylabs.WorkspaceHypothesis{}}
		if w.recordHypotheses(response) == nil {
			t.Fatal("missing reason accepted")
		}
		response.Reason = "Newer source replaced this captured snapshot"
		if err := w.recordHypotheses(response); err != nil {
			t.Fatal(err)
		}
		got := <-pending.reply
		if got.Status != status || got.Reason != response.Reason {
			t.Fatalf("lost outcome: %+v", got)
		}
		if w.recordHypotheses(response) == nil {
			t.Fatal("accepted repeated single-use request")
		}
	}
}
