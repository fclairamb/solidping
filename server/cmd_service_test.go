package main

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultServiceDir(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := map[string]string{"ProgramData": `D:\Data`, "HOME": "/Users/alice"}
	getenv := func(key string) string { return env[key] }
	r.Equal(`D:\Data\SolidPing`, defaultServiceDir(goosWindows, false, getenv))
	r.Equal(`C:\ProgramData\SolidPing`, defaultServiceDir(goosWindows, false, func(string) string { return "" }))
	r.Equal("/Library/Application Support/SolidPing", defaultServiceDir(goosDarwin, true, getenv))
	r.Equal("/Users/alice/Library/Application Support/SolidPing", defaultServiceDir(goosDarwin, false, getenv))
	r.Equal("/var/lib/solidping", defaultServiceDir(goosLinux, true, getenv))
	r.Equal("/Users/alice/.local/share/solidping", defaultServiceDir(goosLinux, false, getenv))
	env["XDG_DATA_HOME"] = "/data/alice"
	r.Equal("/data/alice/solidping", defaultServiceDir(goosLinux, false, getenv))
}

func TestDefaultServiceName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "SolidPing", defaultServiceName(goosWindows))
	require.Equal(t, "io.solidping", defaultServiceName(goosDarwin))
	require.Equal(t, "solidping", defaultServiceName(goosLinux))
}

func TestServiceRunArgs(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{"service", "run", "--name", "SolidPing EU", "--dir", `C:\ProgramData\SolidPing`},
		serviceRunArgs("SolidPing EU", `C:\ProgramData\SolidPing`),
	)
}

func TestServiceCommandUnsupported(t *testing.T) {
	t.Parallel()

	if serviceSupported(runtime.GOOS) {
		t.Skip("the service commands are real on Windows, macOS and Linux")
	}

	for _, args := range [][]string{
		{"install"}, {"install", "--dir", t.TempDir()}, {"uninstall"}, {"start"}, {"stop"}, {"run"},
	} {
		err := buildRootCommand().Run(context.Background(), append([]string{"solidping", "service"}, args...))
		require.ErrorIs(t, err, errServiceUnsupported, args)
	}

	service := buildRootCommand().Command("service")
	require.NotNil(t, service)
	require.True(t, service.Hidden)
}
