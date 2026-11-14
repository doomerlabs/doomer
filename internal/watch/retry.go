package watch

// Each prompt requests a new review, even when its snapshot is unchanged.
// Remember this watcher's submitted request so polling cannot submit it repeatedly.
// A restarted watcher may resubmit the request; the API owns deduplication.
func newPromptRequestsReview(root, path string, previous *State) bool {
	var state State
	return Read(StatePath(root, path), &state) == nil && !state.RequestedAt.IsZero() &&
		!state.Ambiguous && !hasActiveEdit(state) && state.Owner.Session != "" && state.Owner.Turn != "" &&
		(previous == nil || !sameReviewRequest(state, *previous))
}

// A transport retry keeps the authorization granted when this edit was first
// observed. Moving to another turn does not revoke it, but editing again does.
func sameReviewRequest(current, original State) bool {
	return current.Owner == original.Owner && current.Fingerprint == original.Fingerprint &&
		current.RequestedAt.Equal(original.RequestedAt) && !current.RequestedAt.IsZero()
}

func retryReviewRequested(root, path, fingerprint string, original State) bool {
	var current State
	return Read(StatePath(root, path), &current) == nil && sameReviewRequest(current, original) &&
		!current.Ambiguous && !hasActiveEdit(current) && current.Fingerprint == fingerprint
}

func hasActiveEdit(state State) bool {
	for _, operation := range state.Active {
		if operation.Attributable || operation.Owner != state.Owner {
			return true
		}
	}
	return false
}
