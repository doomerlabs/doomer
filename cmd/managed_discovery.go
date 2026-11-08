package cmd

import "github.com/doomerlabs/doomer/internal/managed"

const discoveryStart = "<!-- doomer-review:start -->"
const discoveryEnd = "<!-- doomer-review:end -->"

func managedDiscoveryInstructions(repo, name string) ([]byte, []byte, error) {
	return managed.RepositoryDiscoveryInstructions(repo, name)
}
