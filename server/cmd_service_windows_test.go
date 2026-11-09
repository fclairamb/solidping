//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

var errServeBoom = errors.New("serve boom")

// executeHandler runs handler.Execute in the background and returns the
// request channel, the status channel and a channel carrying its result.
func executeHandler(handler *serviceHandler) (chan svc.ChangeRequest, chan svc.Status, chan [2]any) {
	requests := make(chan svc.ChangeRequest)
	status := make(chan svc.Status, 8)
	result := make(chan [2]any, 1)

	go func() {
		specific, code := handler.Execute(nil, requests, status)
		result <- [2]any{specific, code}
	}()

	return requests, status, result
}

func TestServiceHandlerStopCancelsRun(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := &serviceHandler{run: func(ctx context.Context) error {
		<-ctx.Done()

		return nil
	}}
	requests, status, result := executeHandler(handler)

	r.Equal(svc.StartPending, (<-status).State)
	r.Equal(svc.Running, (<-status).State)

	requests <- svc.ChangeRequest{Cmd: svc.Stop}

	r.Equal(svc.StopPending, (<-status).State)
	r.Equal([2]any{false, uint32(0)}, <-result)
	r.NoError(handler.err)
}

func TestServiceHandlerRunFailureExitsWithError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := &serviceHandler{run: func(context.Context) error { return errServeBoom }}
	_, _, result := executeHandler(handler)

	select {
	case got := <-result:
		r.Equal([2]any{true, uint32(1)}, got)
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after run failed")
	}

	r.ErrorIs(handler.err, errServeBoom)
}

// requireElevated skips a test that writes inside a folder only SYSTEM and
// Administrators can use, which needs an elevated token (CI runners have one).
func requireElevated(t *testing.T) {
	t.Helper()

	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated (administrator) prompt")
	}
}

// mkdirWithSDDL creates dir with the access rules of sddl.
func mkdirWithSDDL(t *testing.T, dir, sddl string) {
	t.Helper()
	r := require.New(t)

	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	r.NoError(err)

	path, err := windows.UTF16PtrFromString(dir)
	r.NoError(err)

	r.NoError(windows.CreateDirectory(path, &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}))
}

// mkJunction creates a directory junction at link pointing to target, which
// unlike a symbolic link needs no privilege.
func mkJunction(t *testing.T, link, target string) {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	require.NoError(t, err, string(out))
}

// TestServiceDirReusesPreviousInstall is the positive control of
// TestServiceDirRefusesUntrustedFolder: a folder left by a previous install,
// with files and folders added inside since, is accepted again.
func TestServiceDirReusesPreviousInstall(t *testing.T) {
	t.Parallel()
	requireElevated(t)
	r := require.New(t)

	dir := filepath.Join(t.TempDir(), "data")
	r.NoError(createServiceDir(dir))
	r.NoError(os.MkdirAll(filepath.Join(dir, serviceLogDir), 0o750))
	r.NoError(os.WriteFile(filepath.Join(dir, "config.yml"), []byte("server: {}\n"), 0o600))
	r.NoError(os.WriteFile(filepath.Join(dir, serviceLogDir, "solidping.log"), nil, 0o600))

	r.NoError(createServiceDir(dir))
}

func TestServiceDirRefusesUntrustedFolder(t *testing.T) {
	t.Parallel()
	requireElevated(t)

	tests := []struct {
		name    string
		prepare func(t *testing.T, dir string)
		want    error
	}{
		{
			name:    "inherits the parent's access rules",
			prepare: func(t *testing.T, dir string) { t.Helper(); require.NoError(t, os.Mkdir(dir, 0o700)) },
			want:    errDataDirInherits,
		},
		{
			name: "grants access to Users",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				mkdirWithSDDL(t, dir, serviceDirSDDL+"(A;OICI;FA;;;BU)")
			},
			want: errDataDirGrant,
		},
		{
			name: "is a file",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(dir, nil, 0o600))
			},
			want: errDataDirNotFolder,
		},
		{
			name: "is a junction to a protected folder",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "target")
				require.NoError(t, createServiceDir(target))
				mkJunction(t, dir, target)
			},
			want: errDataDirReparse,
		},
		{
			name: "holds a junction",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, createServiceDir(dir))
				mkJunction(t, filepath.Join(dir, "logs"), t.TempDir())
			},
			want: errDataDirReparse,
		},
		{
			name: "holds a file that grants access to Users",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				r := require.New(t)
				r.NoError(createServiceDir(dir))

				file := filepath.Join(dir, "config.local.yml")
				r.NoError(os.WriteFile(file, nil, 0o600))

				descriptor, err := windows.SecurityDescriptorFromString("D:(A;;FA;;;BU)")
				r.NoError(err)
				dacl, _, err := descriptor.DACL()
				r.NoError(err)
				r.NoError(windows.SetNamedSecurityInfo(file, windows.SE_FILE_OBJECT,
					windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil))
			},
			want: errDataDirGrant,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			dir := filepath.Join(t.TempDir(), "data")
			tt.prepare(t, dir)

			err := createServiceDir(dir)
			r.ErrorIs(err, errUntrustedDataDir)
			r.ErrorIs(err, tt.want)
		})
	}
}

func TestServiceDirRefusesUntrustedOwner(t *testing.T) {
	t.Parallel()
	requireElevated(t)
	r := require.New(t)

	dir := filepath.Join(t.TempDir(), "data")
	r.NoError(createServiceDir(dir))

	trust, err := defaultServiceDirTrust()
	r.NoError(err)
	r.NoError(checkServiceDir(dir, trust))

	// The folder's owner (the test's user, or Administrators) is not
	// LocalService, so it stands for a folder another user created.
	localService, err := windows.CreateWellKnownSid(windows.WinLocalServiceSid)
	r.NoError(err)

	trust.owners = []*windows.SID{localService}
	err = checkServiceDir(dir, trust)
	r.ErrorIs(err, errUntrustedDataDir)
	r.ErrorIs(err, errDataDirOwner)
}
