//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	// serviceDirSDDL gives full control to SYSTEM and Administrators only, passed
	// down to everything inside, and blocks inheritance from ProgramData, whose
	// ACL lets every local user read: config.yml and the database hold secrets.
	serviceDirSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

	// serviceStopWait is the wait hint reported while the server shuts down.
	serviceStopWait = 30 * time.Second

	serviceRestartDelay   = 5 * time.Second
	serviceRestartCount   = 3
	serviceResetPeriodSec = 24 * 60 * 60
)

var (
	errServiceExists      = errors.New("service already exists")
	errUntrustedDataDir   = errors.New("data folder is not safe for the service")
	errDataDirReparse     = errors.New("is a link or a junction")
	errDataDirNotFolder   = errors.New("is not a folder")
	errDataDirOwner       = errors.New("is owned by")
	errDataDirInherits    = errors.New("inherits the access rules of its parent folder")
	errDataDirNoDACL      = errors.New("has no access rules, so anyone can use it")
	errDataDirGrant       = errors.New("grants access to")
	errServiceStopTimeout = errors.New("service did not stop")
	errNotUnderSCM        = errors.New("`solidping service run` is started by the Service Control Manager; " +
		"use `solidping serve` to run SolidPing in a console")
)

func serviceInstall(_ context.Context, name, dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate solidping.exe: %w", err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve data folder %s: %w", dir, err)
	}

	manager, err := connectServiceManager()
	if err != nil {
		return err
	}
	defer func() { _ = manager.Disconnect() }()

	if existing, openErr := manager.OpenService(name); openErr == nil {
		_ = existing.Close()

		return fmt.Errorf("%w: %q; remove it first with `solidping service uninstall`", errServiceExists, name)
	}

	if dirErr := createServiceDir(absDir); dirErr != nil {
		return dirErr
	}

	service, err := manager.CreateService(name, exe, mgr.Config{
		StartType:   mgr.StartAutomatic,
		DisplayName: name,
		Description: "SolidPing monitoring server",
	}, serviceRunArgs(name, absDir)...)
	if err != nil {
		return fmt.Errorf("create service %q: %w", name, err)
	}
	defer func() { _ = service.Close() }()

	if err := setServiceRecovery(service); err != nil {
		_ = service.Delete()

		return err
	}

	_, _ = fmt.Fprintf(os.Stdout, "Service %q installed.\n", name)
	_, _ = fmt.Fprintf(os.Stdout, "Configuration: %s\n", filepath.Join(absDir, "config.yml"))
	_, _ = fmt.Fprintf(os.Stdout, "Logs:          %s\n", filepath.Join(absDir, serviceLogDir, "solidping.log"))
	_, _ = fmt.Fprintln(os.Stdout, "Start it with: solidping service start")

	return nil
}

// setServiceRecovery restarts the service when it fails, including when it
// exits with an error rather than crashing.
func setServiceRecovery(service *mgr.Service) error {
	actions := make([]mgr.RecoveryAction, serviceRestartCount)
	for i := range actions {
		actions[i] = mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: serviceRestartDelay}
	}

	if err := service.SetRecoveryActions(actions, serviceResetPeriodSec); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}

	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set recovery on non-crash failures: %w", err)
	}

	return nil
}

// createServiceDir creates the data folder with serviceDirSDDL. A folder that
// already exists must pass checkServiceDir.
func createServiceDir(dir string) error {
	if _, err := os.Lstat(dir); err == nil {
		trust, trustErr := defaultServiceDirTrust()
		if trustErr != nil {
			return trustErr
		}

		return checkServiceDir(dir, trust)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check data folder %s: %w", dir, err)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("create parent of data folder %s: %w", dir, err)
	}

	descriptor, err := windows.SecurityDescriptorFromString(serviceDirSDDL)
	if err != nil {
		return fmt.Errorf("build data folder ACL: %w", err)
	}

	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("data folder %s: %w", dir, err)
	}

	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	// Fails if the folder appeared since the Lstat above, rather than adopting it.
	if err := windows.CreateDirectory(path, attributes); err != nil {
		return fmt.Errorf("create data folder %s: %w", dir, err)
	}

	return nil
}

// serviceDirTrust lists who may own the data folder and its content, and who
// its access rules may grant anything to.
type serviceDirTrust struct {
	owners   []*windows.SID
	grantees []*windows.SID
}

// defaultServiceDirTrust trusts SYSTEM, Administrators and, as an owner, the
// administrator running the install: what a previous install leaves behind.
func defaultServiceDirTrust() (serviceDirTrust, error) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return serviceDirTrust{}, fmt.Errorf("look up the SYSTEM account: %w", err)
	}

	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return serviceDirTrust{}, fmt.Errorf("look up the Administrators group: %w", err)
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return serviceDirTrust{}, fmt.Errorf("look up the current user: %w", err)
	}

	return serviceDirTrust{
		owners:   []*windows.SID{system, admins, user.User.Sid},
		grantees: []*windows.SID{system, admins},
	}, nil
}

// checkServiceDir accepts a data folder that already exists only if no one
// but trust can control it, as a previous install leaves it. Any local user
// may create a folder under ProgramData and own it, and an owner can always
// rewrite the access rules, so re-applying serviceDirSDDL would not take the
// folder back: it must be a protected folder, owned by a trusted account,
// with nothing inside that is a link or belongs to someone else.
func checkServiceDir(dir string, trust serviceDirTrust) error {
	if err := checkServiceDirEntry(dir, trust, true); err != nil {
		return untrustedServiceDir(err)
	}

	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if path == dir {
			return nil
		}

		// Called before WalkDir enters a folder, so a junction is refused
		// before anything behind it is read.
		return checkServiceDirEntry(path, trust, false)
	})
	if err != nil {
		return untrustedServiceDir(err)
	}

	return nil
}

// untrustedServiceDir wraps an error of checkServiceDirEntry, which already
// names the file at fault.
func untrustedServiceDir(err error) error {
	return fmt.Errorf("%w: %w. Delete the folder, or choose another one with --dir", errUntrustedDataDir, err)
}

// checkServiceDirEntry checks one file or folder without following it if it is
// a link or a junction. root also requires a folder whose access rules do not
// inherit from its parent.
func checkServiceDirEntry(path string, trust serviceDirTrust, root bool) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var info windows.ByHandleFileInformation
	if infoErr := windows.GetFileInformationByHandle(handle, &info); infoErr != nil {
		return fmt.Errorf("read the attributes of %s: %w", path, infoErr)
	}

	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%s %w", path, errDataDirReparse)
	}

	if root && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("%s %w", path, errDataDirNotFolder)
	}

	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the access rules of %s: %w", path, err)
	}

	return checkServiceDirDescriptor(path, descriptor, trust, root)
}

func checkServiceDirDescriptor(
	path string, descriptor *windows.SECURITY_DESCRIPTOR, trust serviceDirTrust, root bool,
) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read the owner of %s: %w", path, err)
	}

	if !containsSID(trust.owners, owner) {
		return fmt.Errorf("%s %w %s", path, errDataDirOwner, accountName(owner))
	}

	if root {
		control, _, controlErr := descriptor.Control()
		if controlErr != nil {
			return fmt.Errorf("read the access rules of %s: %w", path, controlErr)
		}

		if control&windows.SE_DACL_PROTECTED == 0 {
			return fmt.Errorf("%s %w", path, errDataDirInherits)
		}
	}

	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read the access rules of %s: %w", path, err)
	}

	if dacl == nil {
		return fmt.Errorf("%s %w", path, errDataDirNoDACL)
	}

	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read the access rules of %s: %w", path, err)
		}

		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if !containsSID(trust.grantees, sid) {
				return fmt.Errorf("%s %w %s", path, errDataDirGrant, accountName(sid))
			}
		default:
			// Object and callback rules lay out their SID differently:
			// refuse them rather than read it.
			return fmt.Errorf("%s %w an access rule of type %d", path, errDataDirGrant, ace.Header.AceType)
		}
	}

	return nil
}

func containsSID(sids []*windows.SID, sid *windows.SID) bool {
	for _, candidate := range sids {
		if candidate.Equals(sid) {
			return true
		}
	}

	return false
}

// accountName names an account for an error message, falling back to its SID.
func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}

	if domain == "" {
		return account
	}

	return domain + `\` + account
}

func serviceUninstall(_ context.Context, name string) error {
	return withService(name, func(service *mgr.Service) error {
		if err := stopService(name, service); err != nil {
			return err
		}

		if err := service.Delete(); err != nil {
			return fmt.Errorf("delete service %q: %w", name, err)
		}

		_, _ = fmt.Fprintf(os.Stdout, "Service %q removed. The data folder is kept.\n", name)

		return nil
	})
}

func serviceStart(_ context.Context, name string) error {
	return withService(name, func(service *mgr.Service) error {
		if err := service.Start(); err != nil {
			return fmt.Errorf("start service %q: %w", name, err)
		}

		_, _ = fmt.Fprintf(os.Stdout, "Service %q started.\n", name)

		return nil
	})
}

func serviceStop(_ context.Context, name string) error {
	return withService(name, func(service *mgr.Service) error {
		if err := stopService(name, service); err != nil {
			return err
		}

		_, _ = fmt.Fprintf(os.Stdout, "Service %q stopped.\n", name)

		return nil
	})
}

// stopService asks the service to stop, unless it already is or is stopping,
// and waits until it has.
func stopService(name string, service *mgr.Service) error {
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("query service %q: %w", name, err)
	}

	if status.State != svc.Stopped && status.State != svc.StopPending {
		if status, err = service.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop service %q: %w", name, err)
		}
	}

	// The server gets serviceStopWait to close; leave a margin past it.
	deadline := time.Now().Add(2 * serviceStopWait)
	for status.State != svc.Stopped {
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %q within %s", errServiceStopTimeout, name, 2*serviceStopWait)
		}

		time.Sleep(300 * time.Millisecond)

		if status, err = service.Query(); err != nil {
			return fmt.Errorf("query service %q: %w", name, err)
		}
	}

	return nil
}

func withService(name string, action func(*mgr.Service) error) error {
	manager, err := connectServiceManager()
	if err != nil {
		return err
	}
	defer func() { _ = manager.Disconnect() }()

	service, err := manager.OpenService(name)
	if err != nil {
		return fmt.Errorf("open service %q: %w", name, err)
	}
	defer func() { _ = service.Close() }()

	return action(service)
}

func connectServiceManager() (*mgr.Mgr, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("connect to the Service Control Manager (run from an administrator prompt): %w", err)
	}

	return manager, nil
}

// serviceRun is what the Service Control Manager launches. It enters the data
// folder (see enterServiceDir) and runs `serve` until the service is stopped.
func serviceRun(_ context.Context, name, dir string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("detect the service context: %w", err)
	}

	if !isService {
		return errNotUnderSCM
	}

	logOut, err := enterServiceDir(dir)
	if err != nil {
		return err
	}

	handler := &serviceHandler{run: func(ctx context.Context) error { return serveTo(ctx, logOut) }}
	if err := svc.Run(name, handler); err != nil {
		return fmt.Errorf("run service %q: %w", name, err)
	}

	return handler.err
}

// serviceHandler bridges the Service Control Manager to run: Stop and Shutdown
// cancel its context, the same graceful path as SIGTERM.
type serviceHandler struct {
	run func(ctx context.Context) error
	err error
}

func (h *serviceHandler) Execute(
	_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status,
) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- h.run(ctx) }()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case h.err = <-done:
			return h.exitCode()
		case request := <-requests:
			switch request.Cmd { //nolint:exhaustive // Only these commands are accepted.
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: uint32(serviceStopWait / time.Millisecond)}

				cancel()

				h.err = <-done

				return h.exitCode()
			}
		}
	}
}

// exitCode reports a failed run as service-specific exit code 1, which the
// recovery actions treat as a failure and restart.
func (h *serviceHandler) exitCode() (bool, uint32) {
	if h.err != nil {
		return true, 1
	}

	return false, 0
}
