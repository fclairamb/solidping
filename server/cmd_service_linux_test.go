//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceRunOutsideSystemd(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")

	require.ErrorIs(t, serviceRun(context.Background(), "solidping", t.TempDir()), errNotUnderSystemd)
}

func TestCreateSystemdServiceDir(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dir := filepath.Join(t.TempDir(), "parent", "solidping")
	r.NoError(createSystemdServiceDir(dir, -1))

	info, err := os.Stat(dir)
	r.NoError(err)
	r.Equal(os.FileMode(0o700), info.Mode().Perm())
	r.NoDirExists(filepath.Join(dir, "logs"), "logs go to the journal")

	// An existing folder keeps its mode.
	r.NoError(os.Chmod(dir, 0o750))
	r.NoError(createSystemdServiceDir(dir, -1))
	info, err = os.Stat(dir)
	r.NoError(err)
	r.Equal(os.FileMode(0o750), info.Mode().Perm())
}

func TestOthersCanExecute(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(othersCanExecute("/bin/sh"))

	private := filepath.Join(t.TempDir(), "private")
	r.NoError(os.Mkdir(private, 0o750))

	exe := filepath.Join(private, "solidping")
	r.NoError(os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755))
	r.False(othersCanExecute(exe))
}

type commandCall struct {
	name string
	args []string
}

// fakeHost replaces the system calls of the Linux service commands: a booted
// systemd, an unprivileged user whose configuration folder is a temp folder,
// and a command runner that records its calls and fails the ones in failing.
type fakeHost struct {
	calls   []commandCall
	failing map[string]string
}

func (h *fakeHost) run(_ context.Context, name string, args ...string) ([]byte, error) {
	h.calls = append(h.calls, commandCall{name: name, args: args})

	if out, ok := h.failing[name]; ok {
		return []byte(out + "\n"), errFakeCommand
	}

	return nil, nil
}

func (h *fakeHost) called(name string) []commandCall {
	var out []commandCall

	for _, call := range h.calls {
		if call.name == name {
			out = append(out, call)
		}
	}

	return out
}

var errFakeCommand = errors.New("command failed")

func newFakeHost(t *testing.T, root bool) *fakeHost {
	t.Helper()

	host := &fakeHost{failing: map[string]string{}}
	tmp := t.TempDir()

	oldEuid, oldExe, oldLookup, oldChown := geteuid, executablePath, lookupUser, chownPath
	oldRun, oldRunPath, oldSystemDir := runCommand, systemdRunPath, systemdSystemDir

	t.Cleanup(func() {
		geteuid, executablePath, lookupUser, chownPath = oldEuid, oldExe, oldLookup, oldChown
		runCommand, systemdRunPath, systemdSystemDir = oldRun, oldRunPath, oldSystemDir
	})

	euid := 1000
	if root {
		euid = 0
	}

	geteuid = func() int { return euid }
	executablePath = func() (string, error) { return "/bin/sh", nil }
	lookupUser = func(string) (*user.User, error) { return &user.User{Uid: "4242"}, nil }
	chownPath = func(string, int, int) error { return nil }
	runCommand = host.run
	systemdRunPath = tmp
	systemdSystemDir = filepath.Join(tmp, "system")

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))

	return host
}

func TestNewSystemdJob(t *testing.T) {
	r := require.New(t)

	_, err := newSystemdJob("bad name")
	r.ErrorIs(err, errSystemdUnitName)

	newFakeHost(t, false)
	job, err := newSystemdJob("solidping")
	r.NoError(err)
	r.False(job.root)
	r.Equal(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd/user/solidping.service"), job.unitPath)
	r.Equal("solidping.service", job.unit())
	r.Contains(job.kind(), "user unit")
	r.Equal([]string{"--user", "-u", "x"}, job.managerArgs("-u", "x"))

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	_, err = newSystemdJob("solidping")
	r.ErrorContains(err, "locate the configuration folder")

	newFakeHost(t, true)
	job, err = newSystemdJob("solidping")
	r.NoError(err)
	r.True(job.root)
	r.Equal(filepath.Join(systemdSystemDir, "solidping.service"), job.unitPath)
	r.Contains(job.kind(), "system unit")
	r.Equal([]string{"-u", "x"}, job.managerArgs("-u", "x"))
}

func TestSystemdJobSystemctl(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	host := newFakeHost(t, false)
	job := systemdJob{name: "solidping"}

	r.NoError(job.systemctl(context.Background(), "start", "solidping.service"))
	r.Equal([]commandCall{{name: "systemctl", args: []string{"--user", "start", "solidping.service"}}}, host.calls)

	host.failing["systemctl"] = "Unit not found"
	err := job.systemctl(context.Background(), "start", "solidping.service")
	r.ErrorIs(err, errFakeCommand)
	r.ErrorContains(err, "systemctl --user start solidping.service")
	r.ErrorContains(err, "Unit not found")
}

func TestSystemdJobInstalled(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	dir := t.TempDir()

	r.ErrorIs(systemdJob{unitPath: filepath.Join(dir, "missing.service")}.installed(), errServiceNotInstalled)

	unit := filepath.Join(dir, "a.service")
	r.NoError(os.WriteFile(unit, nil, 0o600))
	r.NoError(systemdJob{unitPath: unit}.installed())

	// A file in the place of a folder is neither "installed" nor "not installed".
	err := systemdJob{unitPath: filepath.Join(unit, "b.service")}.installed()
	r.ErrorContains(err, "check ")
	r.NotErrorIs(err, errServiceNotInstalled)
}

func TestServiceInstallUser(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	host := newFakeHost(t, false)
	dataDir := filepath.Join(t.TempDir(), "data")

	r.NoError(serviceInstall(context.Background(), "solidping", dataDir))

	unitPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd/user/solidping.service")
	unit, err := os.ReadFile(unitPath)
	r.NoError(err)
	r.Contains(string(unit), "WantedBy=default.target")
	r.Contains(string(unit), "WorkingDirectory="+dataDir)
	r.DirExists(dataDir)
	r.Equal([]commandCall{
		{name: "systemctl", args: []string{"--user", "daemon-reload"}},
		{name: "systemctl", args: []string{"--user", "enable", "solidping.service"}},
	}, host.calls)

	// Installing twice is refused and leaves the unit alone.
	r.ErrorIs(serviceInstall(context.Background(), "solidping", dataDir), errServiceExists)
}

func TestServiceInstallRoot(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	host := newFakeHost(t, true)
	dataDir := filepath.Join(t.TempDir(), "data")

	var chowned []int

	chownPath = func(_ string, uid, _ int) error {
		chowned = append(chowned, uid)

		return nil
	}

	r.NoError(serviceInstall(context.Background(), "solidping", dataDir))

	unit, err := os.ReadFile(filepath.Join(systemdSystemDir, "solidping.service"))
	r.NoError(err)
	r.Contains(string(unit), "User="+systemdServiceUser)
	r.Equal([]int{4242}, chowned)
	r.Empty(host.called("useradd"))
	r.Len(host.called("systemctl"), 2)
	r.Equal([]string{"daemon-reload"}, host.called("systemctl")[0].args)
}

func TestServiceInstallErrors(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")

	newFakeHost(t, false)
	systemdRunPath = filepath.Join(t.TempDir(), "absent")
	r.ErrorIs(serviceInstall(ctx, "solidping", dataDir), errNoSystemd)

	newFakeHost(t, false)
	executablePath = func() (string, error) { return "", errFakeCommand }
	r.ErrorContains(serviceInstall(ctx, "solidping", dataDir), "locate solidping")

	newFakeHost(t, false)
	r.ErrorIs(serviceInstall(ctx, "solidping", filepath.Join(t.TempDir(), "with space")), errSystemdPath)

	newFakeHost(t, false)
	r.ErrorIs(serviceInstall(ctx, "bad name", dataDir), errSystemdUnitName)

	// The configuration folder is a file: the unit folder cannot be created.
	newFakeHost(t, false)
	blocker := filepath.Join(t.TempDir(), "blocker")
	r.NoError(os.WriteFile(blocker, nil, 0o600))
	t.Setenv("XDG_CONFIG_HOME", blocker)
	r.ErrorContains(serviceInstall(ctx, "solidping", dataDir), "create ")

	// The data folder cannot be created under a file.
	newFakeHost(t, false)
	r.ErrorContains(serviceInstall(ctx, "solidping", filepath.Join(blocker, "data")), "data folder")

	// systemctl refuses to register the unit.
	host := newFakeHost(t, false)
	host.failing["systemctl"] = "boom"
	err := serviceInstall(ctx, "solidping", dataDir)
	r.ErrorIs(err, errFakeCommand)
	r.ErrorContains(err, `register service "solidping"`)

	// The service user cannot be set up.
	newFakeHost(t, true)
	lookupUser = func(string) (*user.User, error) { return nil, errFakeCommand }
	r.ErrorContains(serviceInstall(ctx, "solidping", filepath.Join(t.TempDir(), "d")), "look up user")
}

func TestServiceInstallWriteUnitFails(t *testing.T) { //nolint:paralleltest // system seams
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}

	r := require.New(t)
	newFakeHost(t, true)

	// The system unit folder is read-only: the unit cannot be written.
	r.NoError(os.MkdirAll(systemdSystemDir, 0o755))
	r.NoError(os.Chmod(systemdSystemDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(systemdSystemDir, 0o755) })

	r.ErrorContains(serviceInstall(context.Background(), "solidping", filepath.Join(t.TempDir(), "d")), "write ")
}

func TestServiceOwnerWarnsOfPrivateExecutable(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	newFakeHost(t, true)

	private := filepath.Join(t.TempDir(), "private")
	r.NoError(os.Mkdir(private, 0o750))

	exe := filepath.Join(private, "solidping")
	r.NoError(os.WriteFile(exe, nil, 0o755))

	owner, err := serviceOwner(context.Background(), systemdJob{root: true}, exe, t.TempDir())
	r.NoError(err)
	r.Equal(4242, owner)

	owner, err = serviceOwner(context.Background(), systemdJob{root: false}, exe, t.TempDir())
	r.NoError(err)
	r.Equal(-1, owner)
}

func TestEnsureServiceUser(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	ctx := context.Background()

	host := newFakeHost(t, true)
	uid, err := ensureServiceUser(ctx, "/data")
	r.NoError(err)
	r.Equal(4242, uid)
	r.Empty(host.calls)

	// Unknown user: it is created, then looked up again.
	host = newFakeHost(t, true)
	lookups := 0
	lookupUser = func(name string) (*user.User, error) {
		lookups++
		if lookups == 1 {
			return nil, user.UnknownUserError(name)
		}

		return &user.User{Uid: "999"}, nil
	}
	uid, err = ensureServiceUser(ctx, "/data")
	r.NoError(err)
	r.Equal(999, uid)
	r.Equal(2, lookups)
	r.Len(host.called("useradd"), 1)
	r.Contains(host.called("useradd")[0].args, "/data")

	// useradd fails.
	host = newFakeHost(t, true)
	host.failing["useradd"] = "permission denied"
	lookupUser = func(name string) (*user.User, error) { return nil, user.UnknownUserError(name) }
	_, err = ensureServiceUser(ctx, "/data")
	r.ErrorIs(err, errFakeCommand)
	r.ErrorContains(err, "permission denied")

	// The user still cannot be found after useradd.
	newFakeHost(t, true)
	lookupUser = func(name string) (*user.User, error) { return nil, user.UnknownUserError(name) }
	_, err = ensureServiceUser(ctx, "/data")
	r.ErrorContains(err, "look up user")

	// A uid that is not a number.
	newFakeHost(t, true)
	lookupUser = func(string) (*user.User, error) { return &user.User{Uid: "abc"}, nil }
	_, err = ensureServiceUser(ctx, "/data")
	r.ErrorContains(err, "uid of")
}

func TestCreateSystemdServiceDirOwner(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	newFakeHost(t, true)

	var chowned []int

	chownPath = func(_ string, uid, _ int) error {
		chowned = append(chowned, uid)

		return nil
	}

	dir := filepath.Join(t.TempDir(), "data")
	r.NoError(createSystemdServiceDir(dir, 4242))
	r.Equal([]int{4242}, chowned)

	// An existing folder owned by someone else is kept, with a warning only.
	r.NoError(createSystemdServiceDir(dir, os.Getuid()+1))
	r.Equal([]int{4242}, chowned)

	chownPath = func(string, int, int) error { return errFakeCommand }
	r.ErrorIs(createSystemdServiceDir(filepath.Join(t.TempDir(), "other"), 4242), errFakeCommand)
}

func TestCreateSystemdServiceDirErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	blocker := filepath.Join(t.TempDir(), "blocker")
	r.NoError(os.WriteFile(blocker, nil, 0o600))

	// Stat fails with ENOTDIR, not ENOENT.
	r.ErrorContains(createSystemdServiceDir(filepath.Join(blocker, "data"), -1), "check data folder")

	// A dangling parent cannot be created: its own parent is a file.
	r.ErrorContains(createSystemdServiceDir(filepath.Join(blocker, "a", "b", "data"), -1), "data folder")
}

func TestServiceLifecycle(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	ctx := context.Background()
	host := newFakeHost(t, false)

	// Nothing is installed yet.
	r.ErrorIs(serviceStart(ctx, "solidping"), errServiceNotInstalled)
	r.ErrorIs(serviceStop(ctx, "solidping"), errServiceNotInstalled)
	r.ErrorIs(serviceUninstall(ctx, "solidping"), errServiceNotInstalled)
	r.ErrorIs(serviceStart(ctx, "bad name"), errSystemdUnitName)
	r.ErrorIs(serviceStop(ctx, "bad name"), errSystemdUnitName)
	r.ErrorIs(serviceUninstall(ctx, "bad name"), errSystemdUnitName)
	r.Empty(host.calls)

	unitPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd/user/solidping.service")
	r.NoError(os.MkdirAll(filepath.Dir(unitPath), 0o755))
	r.NoError(os.WriteFile(unitPath, []byte("[Unit]\n"), 0o600))

	r.NoError(serviceStart(ctx, "solidping"))
	r.NoError(serviceStop(ctx, "solidping"))
	r.Equal([]commandCall{
		{name: "systemctl", args: []string{"--user", "start", "solidping.service"}},
		{name: "systemctl", args: []string{"--user", "stop", "solidping.service"}},
	}, host.calls)

	host.failing["systemctl"] = "boom"
	r.ErrorContains(serviceStart(ctx, "solidping"), `start service "solidping"`)
	r.ErrorContains(serviceStop(ctx, "solidping"), `stop service "solidping"`)
	r.ErrorContains(serviceUninstall(ctx, "solidping"), `stop service "solidping"`)
	r.FileExists(unitPath, "a unit that could not be stopped is kept")

	delete(host.failing, "systemctl")
	host.calls = nil
	r.NoError(serviceUninstall(ctx, "solidping"))
	r.NoFileExists(unitPath)
	r.Equal([]commandCall{
		{name: "systemctl", args: []string{"--user", "disable", "--now", "solidping.service"}},
		{name: "systemctl", args: []string{"--user", "daemon-reload"}},
	}, host.calls)
}

func TestServiceUninstallReloadFails(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	host := newFakeHost(t, false)

	unitPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd/user/solidping.service")
	r.NoError(os.MkdirAll(filepath.Dir(unitPath), 0o755))
	r.NoError(os.WriteFile(unitPath, nil, 0o600))

	// Only the second systemctl call (daemon-reload) fails.
	runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		_, _ = host.run(ctx, name, args...)
		if args[len(args)-1] == "daemon-reload" {
			return []byte("reload failed"), errFakeCommand
		}

		return nil, nil
	}

	r.ErrorContains(serviceUninstall(context.Background(), "solidping"), "daemon-reload")
	r.NoFileExists(unitPath)
}

func TestServiceUninstallRemoveFails(t *testing.T) { //nolint:paralleltest // system seams
	r := require.New(t)
	newFakeHost(t, false)

	// The unit is a non-empty folder: it is installed, but cannot be removed.
	unitPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd/user/solidping.service")
	r.NoError(os.MkdirAll(filepath.Join(unitPath, "x"), 0o755))

	r.ErrorContains(serviceUninstall(context.Background(), "solidping"), "remove ")
}

func TestServiceRunMissingDataFolder(t *testing.T) {
	t.Setenv("INVOCATION_ID", "abc")

	err := serviceRun(context.Background(), "solidping", filepath.Join(t.TempDir(), "absent"))
	require.ErrorContains(t, err, "enter data folder")
}
