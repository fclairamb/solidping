//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var (
	errServiceExists       = errors.New("service already exists")
	errServiceNotInstalled = errors.New("service is not installed")
	errNoSystemd           = errors.New("this system does not run systemd; " +
		"start `solidping serve` with your init system instead")
	errNotUnderSystemd = errors.New("`solidping service run` is started by systemd; " +
		"use `solidping serve` to run SolidPing in a terminal")
)

// systemdJob locates the unit of name for the current user: a system unit when
// run as root (sudo), a user unit otherwise.
type systemdJob struct {
	name     string
	root     bool
	unitPath string
}

func newSystemdJob(name string) (systemdJob, error) {
	if err := checkSystemdUnitName(name); err != nil {
		return systemdJob{}, err
	}

	root := os.Geteuid() == 0

	configHome := ""
	if !root {
		var err error
		if configHome, err = os.UserConfigDir(); err != nil {
			return systemdJob{}, fmt.Errorf("locate the configuration folder: %w", err)
		}
	}

	return systemdJob{name: name, root: root, unitPath: systemdUnitPath(root, configHome, name)}, nil
}

func (j systemdJob) unit() string { return j.name + ".service" }

func (j systemdJob) kind() string {
	if j.root {
		return "systemd system unit, starts at boot as user " + systemdServiceUser
	}

	return "systemd user unit, starts at login; use sudo for a system unit that starts at boot"
}

// managerArgs prefixes args with --user for a user unit, the flag systemctl
// and journalctl take to reach the user's manager rather than the system's.
func (j systemdJob) managerArgs(args ...string) []string {
	if j.root {
		return args
	}

	return append([]string{"--user"}, args...)
}

func (j systemdJob) systemctl(ctx context.Context, args ...string) error {
	args = j.managerArgs(args...)

	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}

// installed fails when install has not written the unit.
func (j systemdJob) installed() error {
	if _, err := os.Stat(j.unitPath); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: no %s; install it with `solidping service install`", errServiceNotInstalled, j.unitPath)
	} else if err != nil {
		return fmt.Errorf("check %s: %w", j.unitPath, err)
	}

	return nil
}

func serviceInstall(ctx context.Context, name, dir string) error {
	// sd_booted(3): the folder only exists when systemd is the init system.
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errNoSystemd
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate solidping: %w", err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve data folder %s: %w", dir, err)
	}

	if err = checkSystemdPath(absDir); err != nil {
		return err
	}

	job, err := newSystemdJob(name)
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(job.unitPath); statErr == nil {
		return fmt.Errorf("%w: %s; remove it first with `solidping service uninstall`", errServiceExists, job.unitPath)
	}

	owner, err := serviceOwner(ctx, job, exe, absDir)
	if err != nil {
		return err
	}

	if err = createSystemdServiceDir(absDir, owner); err != nil {
		return err
	}

	if err = os.MkdirAll(filepath.Dir(job.unitPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(job.unitPath), err)
	}

	unit := systemdUnit(append([]string{exe}, serviceRunArgs(name, absDir)...), absDir, job.root)
	// It holds no secret: configuration goes in config.yml, in the data folder.
	if err = os.WriteFile(job.unitPath, unit, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", job.unitPath, err)
	}

	if err = job.systemctl(ctx, "daemon-reload"); err == nil {
		err = job.systemctl(ctx, "enable", job.unit())
	}

	if err != nil {
		return fmt.Errorf("register service %q: %w", name, err)
	}

	printSystemdInstalled(job, absDir)

	return nil
}

func printSystemdInstalled(job systemdJob, absDir string) {
	_, _ = fmt.Fprintf(os.Stdout, "Service %q installed (%s).\n", job.name, job.kind())
	_, _ = fmt.Fprintf(os.Stdout, "Unit:          %s\n", job.unitPath)
	_, _ = fmt.Fprintf(os.Stdout, "Configuration: %s\n", filepath.Join(absDir, "config.yml"))
	_, _ = fmt.Fprintf(os.Stdout, "Logs:          journalctl %s\n", strings.Join(job.managerArgs("-u", job.name), " "))
	_, _ = fmt.Fprintln(os.Stdout, "Start it with: solidping service start")

	if !job.root {
		_, _ = fmt.Fprintln(os.Stdout, "To start it at boot rather than at login: loginctl enable-linger")
	}
}

// serviceOwner is the uid a system unit runs as, systemdServiceUser, or -1 for
// a user unit, which runs as the user who already owns what install creates.
func serviceOwner(ctx context.Context, job systemdJob, exe, absDir string) (int, error) {
	if !job.root {
		return -1, nil
	}

	owner, err := ensureServiceUser(ctx, absDir)
	if err != nil {
		return 0, err
	}

	if !othersCanExecute(exe) {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: user %s cannot run %s (a parent folder is private). "+
			"Move it first, for example to /usr/local/bin, then install again.\n", systemdServiceUser, exe)
	}

	return owner, nil
}

// ensureServiceUser returns the uid of systemdServiceUser, creating it as a
// system account (no login, no home created) when it does not exist.
func ensureServiceUser(ctx context.Context, absDir string) (int, error) {
	account, err := user.Lookup(systemdServiceUser)
	if errors.As(err, new(user.UnknownUserError)) {
		out, addErr := exec.CommandContext(ctx, "useradd", "--system", "--user-group", "--no-create-home",
			"--home-dir", absDir, "--shell", "/usr/sbin/nologin", systemdServiceUser).CombinedOutput()
		if addErr != nil {
			return 0, fmt.Errorf("create user %s: %w: %s", systemdServiceUser, addErr, strings.TrimSpace(string(out)))
		}

		_, _ = fmt.Fprintf(os.Stdout, "Created system user %q.\n", systemdServiceUser)
		account, err = user.Lookup(systemdServiceUser)
	}

	if err != nil {
		return 0, fmt.Errorf("look up user %s: %w", systemdServiceUser, err)
	}

	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return 0, fmt.Errorf("uid of %s: %w", systemdServiceUser, err)
	}

	return uid, nil
}

// othersCanExecute reports whether a user other than the owner can run exe:
// every parent folder must be searchable by others, and exe executable.
func othersCanExecute(exe string) bool {
	info, err := os.Stat(exe)
	if err != nil || info.Mode().Perm()&0o001 == 0 {
		return false
	}

	for dir := filepath.Dir(exe); ; dir = filepath.Dir(dir) {
		info, err = os.Stat(dir)
		if err != nil || info.Mode().Perm()&0o001 == 0 {
			return false
		}

		if dir == "/" {
			return true
		}
	}
}

// createSystemdServiceDir creates the data folder readable by its owner only:
// config.yml and the database hold secrets. owner, when not -1, is the uid the
// service runs as; the folder is handed to it. An existing folder is left as
// is, with a warning when the service user does not own it.
func createSystemdServiceDir(dir string, owner int) error {
	info, statErr := os.Stat(dir)
	if statErr == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && owner != -1 && int(stat.Uid) != owner {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: %s is not owned by user %s, which may not be able to write to it. "+
				"Hand it over with: sudo chown -R %s: %s\n", dir, systemdServiceUser, systemdServiceUser, dir)
		}

		return nil
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("check data folder %s: %w", dir, statErr)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("create parent of data folder %s: %w", dir, err)
	}

	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create data folder %s: %w", dir, err)
	}

	if owner != -1 {
		// -1 keeps the group: root's, which the folder mode shuts out anyway.
		if err := os.Chown(dir, owner, -1); err != nil {
			return fmt.Errorf("hand data folder %s to %s: %w", dir, systemdServiceUser, err)
		}
	}

	return nil
}

func serviceUninstall(ctx context.Context, name string) error {
	job, err := newSystemdJob(name)
	if err != nil {
		return err
	}

	if err = job.installed(); err != nil {
		return err
	}

	// Stops it and removes it from the boot.
	if err = job.systemctl(ctx, "disable", "--now", job.unit()); err != nil {
		return fmt.Errorf("stop service %q: %w", name, err)
	}

	if err = os.Remove(job.unitPath); err != nil {
		return fmt.Errorf("remove %s: %w", job.unitPath, err)
	}

	if err = job.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(os.Stdout, "Service %q removed. The data folder is kept.\n", name)

	return nil
}

func serviceStart(ctx context.Context, name string) error {
	job, err := newSystemdJob(name)
	if err != nil {
		return err
	}

	if err = job.installed(); err != nil {
		return err
	}

	if err = job.systemctl(ctx, "start", job.unit()); err != nil {
		return fmt.Errorf("start service %q: %w", name, err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "Service %q started.\n", name)

	return nil
}

// serviceStop sends SIGTERM to the server and waits until it has stopped
// (systemctl stop blocks). The unit stays enabled, so it starts again at the
// next boot, like a stopped Windows service with automatic start.
func serviceStop(ctx context.Context, name string) error {
	job, err := newSystemdJob(name)
	if err != nil {
		return err
	}

	if err = job.installed(); err != nil {
		return err
	}

	if err = job.systemctl(ctx, "stop", job.unit()); err != nil {
		return fmt.Errorf("stop service %q: %w", name, err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "Service %q stopped.\n", name)

	return nil
}

// serviceRun is what systemd launches. It enters the data folder, so
// config.yml and the SQLite database resolve there, and runs `serve`, which
// stops gracefully on the SIGTERM systemd sends. Logs and crash output go to
// stdout and stderr, which systemd sends to the journal. A failed run exits
// non-zero, so systemd restarts it.
func serviceRun(ctx context.Context, _, dir string) error {
	// systemd sets INVOCATION_ID for every unit it starts.
	if os.Getenv("INVOCATION_ID") == "" {
		return errNotUnderSystemd
	}

	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("enter data folder %s: %w", dir, err)
	}

	return serveTo(ctx, os.Stdout)
}
