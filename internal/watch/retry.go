package watch

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
