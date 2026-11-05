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
func TestRevisionGroupsDoNotCrossSessions(t *testing.T) {
	first := State{Owner: Owner{Session: "smoke-chat", Turn: "first"}, RequestedAt: time.Unix(100, 0)}
	original := revisionForEdit(revision{}, "same-content", first)
	second := first
	second.Owner = Owner{Session: "footer-chat", Turn: "second"}
	next := revisionForEdit(original, "same-content", second)
	if next.Group == original.Group || next.Number != 1 || next.ID == original.ID {
		t.Fatalf("unrelated chat reused prior group: before=%+v after=%+v", original, next)
	}
}

func TestLegacyRevisionWithoutSessionStartsSeparateGroup(t *testing.T) {
	old := revision{ID: "old", Group: "existing", Number: 9, Digest: "same-content"}
	next := revisionForEdit(old, "same-content", State{Owner: Owner{Session: "chat", Turn: "turn"}})
	if next.ID == old.ID || next.Group == old.Group || next.Number != 1 || next.RequestKey == "" {
		t.Fatalf("unscoped legacy group was reused: %+v", next)
	}
}
