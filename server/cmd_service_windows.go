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
// already exists is left as is.
func createServiceDir(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
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
	if err := windows.CreateDirectory(path, attributes); err != nil {
		return fmt.Errorf("create data folder %s: %w", dir, err)
	}

	return nil
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
