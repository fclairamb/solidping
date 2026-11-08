package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path"
)

const (
	// launchdThrottleSec is how long launchd waits before restarting a failed run.
	launchdThrottleSec = 5
	// launchdExitTimeoutSec is how long launchd lets the server close after
	// SIGTERM before it sends SIGKILL.
	launchdExitTimeoutSec = 30
)

// launchdDomain is where the job lives: the system domain for root (a daemon,
// started at boot), the user's GUI domain otherwise (an agent, started at login).
func launchdDomain(uid int) string {
	if uid == 0 {
		return "system"
	}

	return fmt.Sprintf("gui/%d", uid)
}

// launchdPlistPath is where install writes the job's property list.
func launchdPlistPath(uid int, home, label string) string {
	if uid == 0 {
		return path.Join("/Library/LaunchDaemons", label+".plist")
	}

	return path.Join(home, "Library/LaunchAgents", label+".plist")
}

// launchdPlist renders the job's property list. The job starts when loaded
// (at boot or login once installed), restarts after a failed run but not after
// a clean stop, and gets launchdExitTimeoutSec to shut down. stdout and stderr
// only receive what happens before `service run` takes over the logs.
func launchdPlist(label string, args []string, absDir string) []byte {
	var buf bytes.Buffer

	str := func(indent, value string) {
		buf.WriteString(indent + "<string>")
		_ = xml.EscapeText(&buf, []byte(value)) // A bytes.Buffer never fails.
		buf.WriteString("</string>\n")
	}

	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
`)
	str("\t", label)
	buf.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")

	for _, arg := range args {
		str("\t\t", arg)
	}

	buf.WriteString("\t</array>\n\t<key>WorkingDirectory</key>\n")
	str("\t", absDir)
	buf.WriteString("\t<key>StandardOutPath</key>\n")
	str("\t", path.Join(absDir, "logs", "launchd.log"))
	buf.WriteString("\t<key>StandardErrorPath</key>\n")
	str("\t", path.Join(absDir, "logs", "launchd.log"))
	fmt.Fprintf(&buf, `	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>%d</integer>
	<key>ExitTimeOut</key>
	<integer>%d</integer>
</dict>
</plist>
`, launchdThrottleSec, launchdExitTimeoutSec)

	return buf.Bytes()
}
