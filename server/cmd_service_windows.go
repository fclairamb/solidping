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
	"golang.org/x/sys/windows/svc/eventlog"
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

	// trustedInstallerSID owns the system files, C:\Program Files included.
	trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

	// fileDeleteChild lets a folder's grantee delete anything inside it.
	fileDeleteChild = 0x40

	// replaceRights let a grantee replace a folder, or what it holds: delete or
	// rename it, delete its children, or take it over. Rights that only add a
	// new entry are left out: C:\ and ProgramData give them to every user, and
	// an existing entry cannot be replaced with them.
	replaceRights = windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | fileDeleteChild | windows.GENERIC_ALL

	// writeRights also cover writing a file, or adding one to a folder: on the
	// binary and its folder, where a planted DLL would run as the service too.
	writeRights = replaceRights | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.GENERIC_WRITE

	windowsBinaryFix = `Move solidping.exe to C:\Program Files\SolidPing (create that folder from an administrator prompt)`
)

// entryMode is what checkServiceDirEntry requires of a file or folder.
type entryMode int

const (
	// entryDataDir is the data folder: a protected folder that grants nothing
	// to anyone untrusted.
	entryDataDir entryMode = iota
	// entryInside is a file or folder in the data folder: it grants nothing to
	// anyone untrusted.
	entryInside
	// entryBinary is the binary or its folder: no untrusted account may write it.
	entryBinary
	// entryParent is a parent folder: no untrusted account may replace it or
	// what it holds. Users may read and run it, as in C:\Program Files.
	entryParent
)

var (
	errServiceExists      = errors.New("service already exists")
	errDataDirInherits    = errors.New("inherits the access rules of its parent folder")
	errDataDirNoDACL      = errors.New("has no access rules, so anyone can use it")
	errDataDirGrant       = errors.New("grants access to")
	errPathChangeableBy   = errors.New("can be changed by")
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

	installer, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("look up the current user: %w", err)
	}

	trust, err := newServiceTrust(installer.User.Sid)
	if err != nil {
		return err
	}

	// The service runs it as LocalSystem: only administrators may be able to
	// replace it.
	if exe, err = checkServiceBinary(exe, trust.path); err != nil {
		return err
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

	if dirErr := createServiceDirWith(absDir, &trust); dirErr != nil {
		return dirErr
	}

	// `service run` checks the same paths again, trusting the same installer.
	args := append(serviceRunArgs(name, absDir), "--"+flagServiceInstaller, installer.User.Sid.String())

	service, err := manager.CreateService(name, exe, mgr.Config{
		StartType:   mgr.StartAutomatic,
		DisplayName: name,
		Description: "SolidPing monitoring server",
	}, args...)
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

// createServiceDir is createServiceDirWith, trusting the current user as the
// installer.
func createServiceDir(dir string) error {
	installer, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("look up the current user: %w", err)
	}

	trust, err := newServiceTrust(installer.User.Sid)
	if err != nil {
		return err
	}

	return createServiceDirWith(dir, &trust)
}

// createServiceDirWith creates the data folder with serviceDirSDDL. A folder
// that already exists must pass checkServiceDir. Its parents, the ones that
// install creates included, must pass checkServiceDirParents: a user who can
// replace one can swap the data folder for its own.
func createServiceDirWith(dir string, trust *serviceTrust) error {
	if _, err := os.Lstat(dir); err == nil {
		if parentErr := checkServiceDirParents(dir, trust.path); parentErr != nil {
			return parentErr
		}

		return checkServiceDir(dir, trust.dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check data folder %s: %w", dir, err)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("create parent of data folder %s: %w", dir, err)
	}

	// A folder created under C:\ inherits its Modify right for every user.
	if err := checkServiceDirParents(dir, trust.path); err != nil {
		return err
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

// serviceDirTrust lists who may own a file, and who its access rules may grant
// something to: anything for the data folder, a write right for a parent or
// the binary.
type serviceDirTrust struct {
	owners   []*windows.SID
	grantees []*windows.SID
}

// serviceTrust is what install and `service run` trust: dir for the data
// folder and what it holds, path for the binary and the parents.
type serviceTrust struct {
	dir  serviceDirTrust
	path serviceDirTrust
}

// newServiceTrust trusts SYSTEM, Administrators and, as an owner, the
// administrator who installs the service: what a previous install leaves
// behind. Only they may control the binary and the parents, plus
// TrustedInstaller, which owns the system folders. installer may be nil: a
// service registered before `service run` was given its installer.
func newServiceTrust(installer *windows.SID) (serviceTrust, error) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return serviceTrust{}, fmt.Errorf("look up the SYSTEM account: %w", err)
	}

	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return serviceTrust{}, fmt.Errorf("look up the Administrators group: %w", err)
	}

	trustedInstaller, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		return serviceTrust{}, fmt.Errorf("look up the TrustedInstaller account: %w", err)
	}

	accounts := []*windows.SID{system, admins, trustedInstaller}
	if installer != nil {
		accounts = append(accounts, installer)
	}

	owners := []*windows.SID{system, admins}
	if installer != nil {
		owners = append(owners, installer)
	}

	return serviceTrust{
		dir:  serviceDirTrust{owners: owners, grantees: []*windows.SID{system, admins}},
		path: serviceDirTrust{owners: accounts, grantees: accounts},
	}, nil
}

// checkServiceBinary resolves exe and refuses it when an account outside trust
// could replace it, or plant a file next to it. It returns the resolved path,
// the one to register.
func checkServiceBinary(exe string, trust serviceDirTrust) (string, error) {
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", exe, err)
	}

	folder := filepath.Dir(resolved)

	err = walkTrustedPath(resolved, func(part string) (bool, error) {
		mode := entryParent
		if part == resolved || part == folder {
			mode = entryBinary
		}

		return checkServiceDirEntry(part, trust, mode)
	})
	if err != nil {
		return "", untrustedBinary(err, windowsBinaryFix)
	}

	return resolved, nil
}

// checkServiceDirParents refuses a data folder whose parents an account
// outside trust could replace.
func checkServiceDirParents(dir string, trust serviceDirTrust) error {
	err := walkTrustedPath(filepath.Dir(dir), func(part string) (bool, error) {
		return checkServiceDirEntry(part, trust, entryParent)
	})
	if err != nil {
		return untrustedParent(err)
	}

	return nil
}

// checkServiceRunPaths is what `service run` checks before it enters dir: what
// install checked may have changed since. installer is the SID install was run
// by, empty for a service installed before it was recorded.
func checkServiceRunPaths(dir, installer string) error {
	var installerSID *windows.SID

	if installer != "" {
		sid, err := windows.StringToSid(installer)
		if err != nil {
			return fmt.Errorf("read --%s %s: %w", flagServiceInstaller, installer, err)
		}

		installerSID = sid
	}

	trust, err := newServiceTrust(installerSID)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate solidping.exe: %w", err)
	}

	if _, err = checkServiceBinary(exe, trust.path); err != nil {
		return err
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve data folder %s: %w", dir, err)
	}

	if err = checkServiceDirParents(absDir, trust.path); err != nil {
		return err
	}

	return checkServiceDir(absDir, trust.dir)
}

// checkServiceDir accepts a data folder that already exists only if no one
// but trust can control it, as a previous install leaves it. Any local user
// may create a folder under ProgramData and own it, and an owner can always
// rewrite the access rules, so re-applying serviceDirSDDL would not take the
// folder back: it must be a protected folder, owned by a trusted account,
// with nothing inside that is a link or belongs to someone else.
func checkServiceDir(dir string, trust serviceDirTrust) error {
	if _, err := checkServiceDirEntry(dir, trust, entryDataDir); err != nil {
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
		_, entryErr := checkServiceDirEntry(path, trust, entryInside)

		return entryErr
	})
	if err != nil {
		return untrustedServiceDir(err)
	}

	return nil
}

// checkServiceDirEntry checks one file or folder without following it if it is
// a link or a junction, which the data folder refuses. For the binary and the
// parents, a link made by a trusted owner is reported, for the caller to check
// its target too.
func checkServiceDirEntry(path string, trust serviceDirTrust, mode entryMode) (bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}

	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var info windows.ByHandleFileInformation
	if infoErr := windows.GetFileInformationByHandle(handle, &info); infoErr != nil {
		return false, fmt.Errorf("read the attributes of %s: %w", path, infoErr)
	}

	link := info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	if link && (mode == entryDataDir || mode == entryInside) {
		return false, fmt.Errorf("%s %w", path, errDataDirReparse)
	}

	if mode == entryDataDir && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return false, fmt.Errorf("%s %w", path, errDataDirNotFolder)
	}

	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read the access rules of %s: %w", path, err)
	}

	if link {
		// Only who can write the folder holding it could replace a link:
		// its own access rules do not matter, its owner and target do.
		return true, checkDescriptorOwner(path, descriptor, trust)
	}

	return false, checkServiceDirDescriptor(path, descriptor, trust, mode)
}

func checkDescriptorOwner(path string, descriptor *windows.SECURITY_DESCRIPTOR, trust serviceDirTrust) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read the owner of %s: %w", path, err)
	}

	if !containsSID(trust.owners, owner) {
		return fmt.Errorf("%s %w %s", path, errDataDirOwner, accountName(owner))
	}

	return nil
}

func checkServiceDirDescriptor(
	path string, descriptor *windows.SECURITY_DESCRIPTOR, trust serviceDirTrust, mode entryMode,
) error {
	if err := checkDescriptorOwner(path, descriptor, trust); err != nil {
		return err
	}

	if mode == entryDataDir {
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

		if err := checkACE(path, ace, trust, mode); err != nil {
			return err
		}
	}

	return nil
}

// checkACE refuses an access rule that gives an untrusted account more than
// mode allows. Every rule type keeps its access mask at the same place.
func checkACE(path string, ace *windows.ACCESS_ALLOWED_ACE, trust serviceDirTrust, mode entryMode) error {
	if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
		return nil
	}

	var refused windows.ACCESS_MASK

	switch mode {
	case entryDataDir, entryInside:
		// The data folder refuses any grant, inherit-only ones included.
		refused = ^windows.ACCESS_MASK(0)
	case entryBinary, entryParent:
		// An inherit-only rule, such as the Modify right C:\ passes down to
		// the folders created in it, does not apply to the object itself.
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			return nil
		}

		refused = replaceRights
		if mode == entryBinary {
			refused = writeRights
		}
	}

	if ace.Mask&refused == 0 {
		return nil
	}

	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		// Object and callback rules lay out their SID differently:
		// refuse them rather than read it.
		return fmt.Errorf("%s %w an access rule of type %d", path, errDataDirGrant, ace.Header.AceType)
	}

	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if containsSID(trust.grantees, sid) {
		return nil
	}

	if mode == entryDataDir || mode == entryInside {
		return fmt.Errorf("%s %w %s", path, errDataDirGrant, accountName(sid))
	}

	return fmt.Errorf("%s %w %s", path, errPathChangeableBy, accountName(sid))
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
func serviceRun(_ context.Context, name, dir, installer string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("detect the service context: %w", err)
	}

	if !isService {
		return errNotUnderSCM
	}

	// The log file is in the folder refused: the reason goes to the event
	// log, and the recovery actions retry.
	if err = checkServiceRunPaths(dir, installer); err != nil {
		logServiceEvent(name, err)

		return err
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

// logServiceEvent writes err to the Application event log, under the service's
// name, on a best-effort basis.
func logServiceEvent(name string, err error) {
	events, openErr := eventlog.Open(name)
	if openErr != nil {
		return
	}

	_ = events.Error(1, err.Error())
	_ = events.Close()
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
