package main

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

const (
	// systemdRestartSec is how long systemd waits before restarting a failed run.
	systemdRestartSec = 5
	// systemdStopTimeoutSec is how long systemd lets the server close after
	// SIGTERM before it sends SIGKILL.
	systemdStopTimeoutSec = 30

	// systemdServiceUser is the system account a system unit runs as, created
	// by install when it does not exist.
	systemdServiceUser = "solidping"
)

var (
	errSystemdUnitName = errors.New("invalid systemd unit name")
	errSystemdPath     = errors.New("unsupported character")

	// systemdUnitNameRE is the set systemd accepts in a unit name, without "@"
	// (templates) and "\" (escapes).
	systemdUnitNameRE = regexp.MustCompile(`^[A-Za-z0-9:_.-]{1,240}$`)
)

// checkSystemdUnitName rejects a --name systemd would not accept, or that would
// not stay inside the unit folder.
func checkSystemdUnitName(name string) error {
	if !systemdUnitNameRE.MatchString(name) || strings.Trim(name, ".") == "" {
		return fmt.Errorf("%w %q: use letters, digits, '-', '_', '.' and ':'", errSystemdUnitName, name)
	}

	return nil
}

// checkSystemdPath rejects a path that WorkingDirectory=, ReadWritePaths= or
// Environment= would split or unescape: whitespace, quotes, backslashes and
// control characters.
func checkSystemdPath(p string) error {
	if i := strings.IndexFunc(p, func(r rune) bool {
		return r <= ' ' || r == 0x7f || r == '"' || r == '\'' || r == '\\'
	}); i >= 0 {
		return fmt.Errorf("%w %q in %q: pick a path without spaces, quotes or backslashes", errSystemdPath, p[i], p)
	}

	return nil
}

// systemdSystemDir is the folder of the system units, a variable for the tests.
var systemdSystemDir = "/etc/systemd/system" //nolint:gochecknoglobals // seam replaced by the tests

// systemdUnitPath is where install writes the unit: the system folder for root,
// the user's folder ($XDG_CONFIG_HOME/systemd/user) otherwise.
func systemdUnitPath(root bool, configHome, name string) string {
	if root {
		return path.Join(systemdSystemDir, name+".service")
	}

	return path.Join(configHome, "systemd/user", name+".service")
}

// systemdEscape escapes the specifier character "%", which systemd expands in
// every setting of a unit.
func systemdEscape(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

// systemdQuoteArg quotes one ExecStart= argument: C escapes inside double
// quotes, "%" and "$" doubled so systemd expands neither specifiers nor
// environment variables.
func systemdQuoteArg(arg string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$", "\n", `\n`, "\t", `\t`)

	return `"` + replacer.Replace(arg) + `"`
}

// systemdUnit renders the service unit. A system unit (root) runs as
// systemdServiceUser with the capabilities ICMP checks and a port below 1024
// need, on a read-only system except its data folder. A user unit runs as the
// user, without hardening, which needs privileges a user manager lacks. Both
// restart after a failed run but not after a clean stop, give the server
// systemdStopTimeoutSec to shut down, and log to the journal.
func systemdUnit(args []string, absDir string, root bool) []byte {
	var unit strings.Builder

	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = systemdQuoteArg(arg)
	}

	dir := systemdEscape(absDir)

	unit.WriteString("[Unit]\nDescription=SolidPing monitoring\n")

	if root {
		// Ordering only: a host without PostgreSQL ignores it.
		unit.WriteString("Wants=network-online.target\nAfter=network-online.target postgresql.service\n")
	}

	unit.WriteString("\n[Service]\nType=simple\n")
	fmt.Fprintf(&unit, "ExecStart=%s\n", strings.Join(quoted, " "))
	fmt.Fprintf(&unit, "WorkingDirectory=%s\n", dir)
	fmt.Fprintf(&unit, "Restart=on-failure\nRestartSec=%d\nTimeoutStopSec=%d\n", systemdRestartSec, systemdStopTimeoutSec)

	if root {
		// Without Group=, systemd uses the user's primary group.
		fmt.Fprintf(&unit, `User=%[1]s
Environment=HOME=%[2]s
UMask=0077
AmbientCapabilities=CAP_NET_RAW CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=%[2]s
`, systemdServiceUser, dir)
	}

	unit.WriteString("\n[Install]\n")

	if root {
		unit.WriteString("WantedBy=multi-user.target\n")
	} else {
		unit.WriteString("WantedBy=default.target\n")
	}

	return []byte(unit.String())
}
