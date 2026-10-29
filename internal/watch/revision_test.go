package watch

import (
	"testing"
	"time"
)

func TestRevisionIdentityBelongsToEditNotOnlyContent(t *testing.T) {
	first := State{Owner: Owner{Session: "chat", Turn: "first"}, RequestedAt: time.Unix(100, 0), AgentDiff: "edit", Prompt: "first request"}
	rev := revisionForEdit(revision{}, "same-content", first)
	retry := revisionForEdit(rev, "same-content", first)
	if retry != rev {
		t.Fatal("transport retry changed review identity")
	}
	second := first
	second.Owner.Turn = "second"
	second.Prompt = "repeat the test"
	second.RequestedAt = second.RequestedAt.Add(time.Second)
	repeated := revisionForEdit(rev, "same-content", second)
	if repeated.ID == rev.ID || repeated.Number != rev.Number+1 || repeated.Group != rev.Group {
		t.Fatal("new edit reused an old review ID")
	}
	sameTurnEdit := first
	sameTurnEdit.RequestedAt = first.RequestedAt.Add(time.Second)
	if revisionForEdit(rev, "same-content", sameTurnEdit).ID == rev.ID {
		t.Fatal("undo/reapply in same turn reused identity")
	}
}
func TestLegacyRevisionGetsFreshIdentityWithoutLosingGroup(t *testing.T) {
	old := revision{ID: "old", Group: "existing", Number: 9, Digest: "same-content"}
	next := revisionForEdit(old, "same-content", State{})
	if next.ID == old.ID || next.Group != old.Group || next.Number != 10 || next.RequestKey == "" {
		t.Fatalf("invalid migration: %+v", next)
	}
}
