//go:build !windows

package cmd

import (
	"os"
	"os/exec"
	"syscall"
)

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
