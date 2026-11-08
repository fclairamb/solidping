//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// serviceStopWait leaves a margin past launchdExitTimeoutSec, after which
	// launchd kills the server anyway.
	serviceStopWait = 2 * launchdExitTimeoutSec * time.Second

	quarantineAttr = "com.apple.quarantine"
)

var (
	errServiceExists       = errors.New("service already exists")
	errServiceNotInstalled = errors.New("service is not installed")
	errServiceStopTimeout  = errors.New("service did not stop")
	errNotUnderLaunchd     = errors.New("`solidping service run` is started by launchd; " +
		"use `solidping serve` to run SolidPing in a terminal")
)

// launchdJobRef locates the job of label for the current user: a daemon when
// run as root (sudo), an agent of the user otherwise.
type launchdJobRef struct {
	label     string
	domain    string
	plistPath string
}

func newLaunchdJobRef(label string) (launchdJobRef, error) {
	uid := os.Geteuid()

	home := ""
	if uid != 0 {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return launchdJobRef{}, fmt.Errorf("locate the home folder: %w", err)
		}
	}

	return launchdJobRef{label: label, domain: launchdDomain(uid), plistPath: launchdPlistPath(uid, home, label)}, nil
}

func (j launchdJobRef) target() string { return j.domain + "/" + j.label }

func (j launchdJobRef) kind() string {
	if j.domain == "system" {
		return "launchd daemon, starts at boot"
	}

	return "launchd agent of the current user, starts at login; use sudo for a daemon that starts at boot"
}

// loaded reports whether launchd knows the job, running or not.
func (j launchdJobRef) loaded(ctx context.Context) bool {
	return exec.CommandContext(ctx, "launchctl", "print", j.target()).Run() == nil
}

// installed fails when install has not written the job's plist.
func (j launchdJobRef) installed() error {
	if _, err := os.Stat(j.plistPath); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: no %s; install it with `solidping service install`", errServiceNotInstalled, j.plistPath)
	} else if err != nil {
		return fmt.Errorf("check %s: %w", j.plistPath, err)
	}

	return nil
}

func launchctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}

func serviceInstall(_ context.Context, name, dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate solidping: %w", err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve data folder %s: %w", dir, err)
	}

	job, err := newLaunchdJobRef(name)
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(job.plistPath); statErr == nil {
		return fmt.Errorf("%w: %s; remove it first with `solidping service uninstall`", errServiceExists, job.plistPath)
	}

	// launchd runs a quarantined binary no better than Gatekeeper does.
	if _, xattrErr := unix.Getxattr(exe, quarantineAttr, nil); xattrErr == nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: %s is quarantined and launchd may refuse to start it. "+
			"Clear it with: xattr -d %s %s\n", exe, quarantineAttr, exe)
	}

	if dirErr := createServiceDir(absDir); dirErr != nil {
		return dirErr
	}

	if mkdirErr := os.MkdirAll(filepath.Dir(job.plistPath), 0o755); mkdirErr != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(job.plistPath), mkdirErr)
	}

	plist := launchdPlist(name, append([]string{exe}, serviceRunArgs(name, absDir)...), absDir)
	// launchd ignores a daemon plist that is group- or world-writable; it holds no secret.
	if writeErr := os.WriteFile(job.plistPath, plist, 0o644); writeErr != nil {
		return fmt.Errorf("write %s: %w", job.plistPath, writeErr)
	}

	_, _ = fmt.Fprintf(os.Stdout, "Service %q installed (%s).\n", name, job.kind())
	_, _ = fmt.Fprintf(os.Stdout, "Job:           %s\n", job.plistPath)
	_, _ = fmt.Fprintf(os.Stdout, "Configuration: %s\n", filepath.Join(absDir, "config.yml"))
	_, _ = fmt.Fprintf(os.Stdout, "Logs:          %s\n", filepath.Join(absDir, serviceLogDir, "solidping.log"))
	_, _ = fmt.Fprintln(os.Stdout, "Start it with: solidping service start")

	return nil
}

// createServiceDir creates the data folder readable by its owner only (root
// for a daemon): config.yml and the database hold secrets. A folder that
// already exists is left as is. The log folder is created either way, as
// launchd opens launchd.log in it before solidping starts.
func createServiceDir(dir string) error {
	if _, statErr := os.Stat(dir); errors.Is(statErr, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return fmt.Errorf("create parent of data folder %s: %w", dir, err)
		}

		if err := os.Mkdir(dir, 0o700); err != nil {
			return fmt.Errorf("create data folder %s: %w", dir, err)
		}
	} else if statErr != nil {
		return fmt.Errorf("check data folder %s: %w", dir, statErr)
	}

	if err := os.MkdirAll(filepath.Join(dir, serviceLogDir), 0o750); err != nil {
		return fmt.Errorf("create log folder: %w", err)
	}

	return nil
}

func serviceUninstall(ctx context.Context, name string) error {
	job, err := newLaunchdJobRef(name)
	if err != nil {
		return err
	}

	if err := job.installed(); err != nil {
		return err
	}

	if err := stopService(ctx, job); err != nil {
		return err
	}

	if err := os.Remove(job.plistPath); err != nil {
		return fmt.Errorf("remove %s: %w", job.plistPath, err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "Service %q removed. The data folder is kept.\n", name)

	return nil
}

// serviceStart loads the job, which starts it (RunAtLoad), or starts it again
// when it is already loaded.
func serviceStart(ctx context.Context, name string) error {
	job, err := newLaunchdJobRef(name)
	if err != nil {
		return err
	}

	if err = job.installed(); err != nil {
		return err
	}

	if job.loaded(ctx) {
		err = launchctl(ctx, "kickstart", job.target())
	} else {
		// A job disabled by hand (launchctl disable) would refuse to load.
		if err = launchctl(ctx, "enable", job.target()); err == nil {
			err = launchctl(ctx, "bootstrap", job.domain, job.plistPath)
		}
	}

	if err != nil {
		return fmt.Errorf("start service %q: %w", name, err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "Service %q started.\n", name)

	return nil
}

func serviceStop(ctx context.Context, name string) error {
	job, err := newLaunchdJobRef(name)
	if err != nil {
		return err
	}

	if err := job.installed(); err != nil {
		return err
	}

	if err := stopService(ctx, job); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(os.Stdout, "Service %q stopped.\n", name)

	return nil
}

// stopService unloads the job, which sends SIGTERM to the server, and waits
// until launchd has let it go. The plist stays, so the job loads again at the
// next boot or login, like a stopped Windows service with automatic start.
func stopService(ctx context.Context, job launchdJobRef) error {
	if !job.loaded(ctx) {
		return nil
	}

	if err := launchctl(ctx, "bootout", job.target()); err != nil {
		return fmt.Errorf("stop service %q: %w", job.label, err)
	}

	deadline := time.Now().Add(serviceStopWait)
	for job.loaded(ctx) {
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %q within %s", errServiceStopTimeout, job.label, serviceStopWait)
		}

		time.Sleep(300 * time.Millisecond)
	}

	return nil
}

// serviceRun is what launchd launches. It enters the data folder (see
// enterServiceDir) and runs `serve`, which stops gracefully on the SIGTERM
// launchd sends. A failed run exits non-zero, so launchd restarts it.
func serviceRun(ctx context.Context, _, dir string) error {
	// launchd is PID 1 and the parent of every job it starts.
	if os.Getppid() != 1 {
		return errNotUnderLaunchd
	}

	logOut, err := enterServiceDir(dir)
	if err != nil {
		return err
	}

	return serveTo(ctx, logOut)
}
