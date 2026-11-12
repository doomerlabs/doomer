//go:build !windows

package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

func startTurnProcess(child *exec.Cmd, input []byte) (<-chan error, error) {
	child.Stdin = bytes.NewReader(input)
	var diagnostic turnDiagnostic
	child.Stdout, child.Stderr = io.Discard, &diagnostic
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := child.Wait()
		if err != nil && diagnostic.Len() > 0 {
			err = fmt.Errorf("%w: %s", err, diagnostic.String())
		}
		done <- err
	}()
	return done, nil
}

type turnDiagnostic struct{ bytes.Buffer }

func (b *turnDiagnostic) Write(data []byte) (int, error) {
	n := len(data)
	if remaining := 4096 - b.Len(); remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.Buffer.Write(data)
	}
	return n, nil
}

func startWatchProcess(child *exec.Cmd, logPath string) error {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	child.Stdin = nil
	child.Stdout, child.Stderr = log, log
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return err
	}
	// Reap the child while the launching CLI is alive. Once the CLI exits,
	// the OS adopts the detached watcher. Output never retains the agent's pipes.
	go func() { _ = child.Wait() }()
	return nil
}
