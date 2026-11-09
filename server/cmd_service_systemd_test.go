package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSystemdUnitPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("/etc/systemd/system/solidping.service", systemdUnitPath(true, "", "solidping"))
	r.Equal("/home/alice/.config/systemd/user/solidping.service",
		systemdUnitPath(false, "/home/alice/.config", "solidping"))
}

func TestCheckSystemdUnitName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, name := range []string{"solidping", "solidping-eu", "sp_2.prod", "a:b"} {
		r.NoError(checkSystemdUnitName(name), name)
	}

	for _, name := range []string{"", ".", "..", "../evil", "a/b", "sp@1", "with space", `a\b`} {
		r.ErrorIs(checkSystemdUnitName(name), errSystemdUnitName, name)
	}
}

func TestCheckSystemdPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.NoError(checkSystemdPath("/var/lib/solidping-eu_2%"))

	for _, p := range []string{"/srv/solid ping", "/srv/a\tb", `/srv/"x"`, "/srv/it's", `/srv/a\b`, "/srv/a\nb"} {
		r.ErrorIs(checkSystemdPath(p), errSystemdPath, p)
	}
}

func TestSystemdQuoteArg(t *testing.T) {
	t.Parallel()

	require.Equal(t, `"/opt/a b/\"sp\" \\ 100%% $$HOME"`, systemdQuoteArg(`/opt/a b/"sp" \ 100% $HOME`))
}

func TestSystemdUnitSystem(t *testing.T) {
	t.Parallel()

	dir := "/srv/sp%1"
	args := append([]string{"/usr/local/bin/solidping"}, serviceRunArgs("solidping", dir)...)
	got := string(systemdUnit(args, dir, true))

	require.Equal(t, `[Unit]
Description=SolidPing monitoring
Wants=network-online.target
After=network-online.target postgresql.service

[Service]
Type=simple
ExecStart="/usr/local/bin/solidping" "service" "run" "--name" "solidping" "--dir" "/srv/sp%%1"
WorkingDirectory=/srv/sp%%1
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
User=solidping
Environment=HOME=/srv/sp%%1
UMask=0077
AmbientCapabilities=CAP_NET_RAW CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/srv/sp%%1

[Install]
WantedBy=multi-user.target
`, got)
}

func TestSystemdUnitUser(t *testing.T) {
	t.Parallel()

	dir := "/home/alice/.local/share/solidping"
	args := append([]string{"/home/alice/bin/solidping"}, serviceRunArgs("solidping", dir)...)
	got := string(systemdUnit(args, dir, false))

	require.Equal(t, `[Unit]
Description=SolidPing monitoring

[Service]
Type=simple
ExecStart="/home/alice/bin/solidping" "service" "run" "--name" "solidping" "--dir" "/home/alice/.local/share/solidping"
WorkingDirectory=/home/alice/.local/share/solidping
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

[Install]
WantedBy=default.target
`, got)
}
