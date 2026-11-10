package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLaunchdDomainAndPlistPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("system", launchdDomain(0))
	r.Equal("gui/501", launchdDomain(501))
	r.Equal("/Library/LaunchDaemons/io.solidping.plist", launchdPlistPath(0, "", "io.solidping"))
	r.Equal("/Users/alice/Library/LaunchAgents/io.solidping.plist", launchdPlistPath(501, "/Users/alice", "io.solidping"))
}

func TestLaunchdPlist(t *testing.T) {
	t.Parallel()

	dir := "/Library/Application Support/SolidPing & co"
	got := string(launchdPlist("io.solidping",
		append([]string{"/usr/local/bin/solidping"}, serviceRunArgs("io.solidping", dir)...), dir))

	require.Equal(t, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>io.solidping</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/solidping</string>
		<string>service</string>
		<string>run</string>
		<string>--name</string>
		<string>io.solidping</string>
		<string>--dir</string>
		<string>/Library/Application Support/SolidPing &amp; co</string>
	</array>
	<key>WorkingDirectory</key>
	<string>/Library/Application Support/SolidPing &amp; co</string>
	<key>StandardOutPath</key>
	<string>/Library/Application Support/SolidPing &amp; co/logs/launchd.log</string>
	<key>StandardErrorPath</key>
	<string>/Library/Application Support/SolidPing &amp; co/logs/launchd.log</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>5</integer>
	<key>ExitTimeOut</key>
	<integer>30</integer>
</dict>
</plist>
`, got)
}
