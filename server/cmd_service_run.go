//go:build windows || darwin

package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/logsetup"
	"github.com/fclairamb/solidping/server/internal/rotatelog"
)

const serviceLogDir = "logs"

// enterServiceDir prepares a `service run` process: it enters the data folder,
// so config.yml and the SQLite database resolve there, sends Go crash output to
// logs/crash.log, and returns the rotated log file writer, already slog's
// default. A service has no console, so without them a log line or a panic
// would leave no trace.
func enterServiceDir(dir string) (io.Writer, error) {
	if err := os.Chdir(dir); err != nil {
		return nil, fmt.Errorf("enter data folder %s: %w", dir, err)
	}

	if err := os.MkdirAll(serviceLogDir, 0o750); err != nil {
		return nil, fmt.Errorf("create log folder: %w", err)
	}

	crashLog, err := os.OpenFile(filepath.Join(serviceLogDir, "crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open crash log: %w", err)
	}

	crashErr := debug.SetCrashOutput(crashLog, debug.CrashOptions{})
	_ = crashLog.Close() // SetCrashOutput keeps its own duplicate.

	if crashErr != nil {
		return nil, fmt.Errorf("set crash output: %w", crashErr)
	}

	logOut := rotatelog.New(
		filepath.Join(serviceLogDir, "solidping.log"), rotatelog.DefaultMaxSize, rotatelog.DefaultBackups)
	slog.SetDefault(slog.New(logsetup.NewHandler(
		logOut, config.LogLevelFromEnv(os.Getenv), config.ParseLogFormat(os.Getenv("SP_LOG_FORMAT")))))

	return logOut, nil
}
