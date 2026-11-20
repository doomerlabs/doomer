package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func runSystemMDMUpdate(ctx context.Context, out io.Writer, scheduled bool, channel string) error {
	if os.Geteuid() != 0 {
		return errors.New("managed Doomer updates require macOS root privileges")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if executable != filepath.Join(mdmSystemDir, "bootstrap-doomer") &&
		executable != filepath.Join(mdmSystemDir, "doomer") {
		return errors.New("run the updater installed by the Doomer MDM bootstrap")
	}
	for _, path := range []string{"/Library", mdmSystemDir} {
		if err := checkMDMSystemPath(path, true); err != nil {
			return err
		}
	}
	if err := checkMDMSystemPath(executable, false); err != nil {
		return err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer transport.CloseIdleConnections()
	u := mdmUpdater{
		dir: mdmSystemDir, origin: mdmReleaseOrigin, client: client,
		readChannel: func(ctx context.Context) (string, error) {
			return mdmDeviceChannel(ctx, runMDMSystemCommand, checkMDMSystemPath, mdmPreferences)
		},
		run: runMDMSystemCommand, checkPath: checkMDMSystemPath,
	}
	if !scheduled {
		status, err := u.update(ctx, channel)
		if status != "" {
			fmt.Fprintln(out, status)
		}
		return err
	}
	running, err := os.Stat(executable)
	if err != nil {
		return err
	}
	active := filepath.Join(mdmSystemDir, "doomer")
	backoff := time.Minute
	for !mdmRuntimeReplaced(active, running) {
		status, err := u.update(ctx, "")
		if err != nil {
			status = err.Error()
		}
		_, _ = runMDMSystemCommand(ctx, "/usr/bin/logger", "-t", "doomer-mdm-update", status)
		if mdmRuntimeReplaced(active, running) {
			break
		}
		delay, next := mdmNextCheck(err, backoff, rand.N(time.Hour+1))
		backoff = next
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	// KeepAlive restarts the system launcher, which now selects the active binary.
	return nil
}

func checkMDMSystemPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 ||
		(directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("unsafe managed Doomer path: %s", path)
	}
	return nil
}

func runMDMSystemCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	// Keep the installation lock until Installer itself returns, even if the
	// caller cancels the update. Installer is responsible for its transaction.
	if path != mdmInstaller {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=/var/root", "LC_ALL=C"}
	return cmd.Output()
}
