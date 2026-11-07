package main

import "encoding/json"

// Subscription allowance is not an API dollar estimate. Retain the in-memory
// accounting shape for the runner while omitting unmeasured usage from JSON.
func (r report) MarshalJSON() ([]byte, error) {
	type plainReport report
	var measured *usage
	if r.AuthMode != "subscription" {
		measured = &r.Usage
	}
	return json.Marshal(struct {
		plainReport
		Usage *usage `json:"usage,omitempty"`
	}{plainReport(r), measured})
}
