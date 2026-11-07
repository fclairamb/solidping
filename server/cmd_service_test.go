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

	env := map[string]string{"ProgramData": `D:\Data`}
	r.Equal(`D:\Data\SolidPing`, defaultServiceDir(func(key string) string { return env[key] }))
	r.Equal(`C:\ProgramData\SolidPing`, defaultServiceDir(func(string) string { return "" }))
}

func TestServiceRunArgs(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{"service", "run", "--name", "SolidPing EU", "--dir", `C:\ProgramData\SolidPing`},
		serviceRunArgs("SolidPing EU", `C:\ProgramData\SolidPing`),
	)
}

func TestServiceCommandUnsupportedOffWindows(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == goosWindows {
		t.Skip("the service commands are real on Windows")
	}

	for _, sub := range []string{"install", "uninstall", "start", "stop", "run"} {
		err := buildRootCommand().Run(context.Background(), []string{"solidping", "service", sub})
		require.ErrorIs(t, err, errServiceUnsupported, sub)
	}

	service := buildRootCommand().Command("service")
	require.NotNil(t, service)
	require.True(t, service.Hidden)
}
