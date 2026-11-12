//go:build windows

package cmd

import (
	"fmt"
	"os/exec"
)

func startWatchProcess(_ *exec.Cmd, _ string) error {
	return fmt.Errorf("background watching currently supports macOS and Linux")
}

func startTurnProcess(_ *exec.Cmd, _ []byte) (<-chan error, error) {
	return nil, fmt.Errorf("turn workers currently support macOS and Linux")
}
