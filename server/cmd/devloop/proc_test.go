package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syncBuffer is a goroutine-safe sink for supervisor tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestParseProcSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    procSpec
		wantErr bool
	}{
		{
			name:  "full spec",
			value: "dash0:/repo/web/dash0:bun run dev",
			want:  procSpec{name: "dash0", dir: "/repo/web/dash0", argv: []string{"bun", "run", "dev"}},
		},
		{
			name:  "single word command",
			value: "aux:/tmp:server",
			want:  procSpec{name: "aux", dir: "/tmp", argv: []string{"server"}},
		},
		{name: "missing command", value: "dash0:/repo/web/dash0", wantErr: true},
		{name: "empty command", value: "dash0:/repo/web/dash0:  ", wantErr: true},
		{name: "empty name", value: ":/repo/web/dash0:bun run dev", wantErr: true},
		{name: "empty dir", value: "dash0::bun run dev", wantErr: true},
		{name: "empty value", value: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			got, err := parseProcSpec(tt.value)
			if tt.wantErr {
				r.ErrorIs(err, errInvalidProc)
				return
			}
			r.NoError(err)
			r.Equal(tt.want, got)
		})
	}
}

func TestParseProcSpecsCollectsAllAndFailsFast(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	specs, err := parseProcSpecs([]string{"a:/tmp:x", "b:/tmp:y z"})
	r.NoError(err)
	r.Len(specs, 2)
	r.Equal("a", specs[0].name)
	r.Equal([]string{"y", "z"}, specs[1].argv)

	_, err = parseProcSpecs([]string{"a:/tmp:x", "broken"})
	r.ErrorIs(err, errInvalidProc)
}

// waitDone fetches the supervisor's done channel and waits on it.
func waitDone(t *testing.T, sup *supervisor) {
	t.Helper()
	sup.mu.Lock()
	done := sup.done
	sup.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit in time")
	}
}

func TestSupervisorRelaysOutputAndReportsUnexpectedExit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &syncBuffer{}
	sup := newSupervisor("aux1", t.TempDir(), []string{"sh", "-c", "echo hello from child; exit 3"}, sink)
	r.NoError(sup.start(context.Background()))
	waitDone(t, sup)

	r.Contains(sink.String(), "hello from child")
	r.Contains(sink.String(), "aux1 exited on its own: exit status 3 (not restarted)")
}

func TestSupervisorStopIsQuietAndReapsChild(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &syncBuffer{}
	sup := newSupervisor("aux2", "", []string{"sh", "-c", "echo started; sleep 30"}, sink)
	sup.grace = time.Second
	r.NoError(sup.start(context.Background()))

	sup.mu.Lock()
	cmd := sup.cmd
	sup.mu.Unlock()

	sup.stop()

	r.NotNil(cmd.ProcessState, "child must be reaped once stop returns")
	r.NotContains(sink.String(), "exited on its own")

	sup.mu.Lock()
	r.Nil(sup.cmd)
	sup.mu.Unlock()
}

func TestSupervisorStopKillsAfterGrace(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &syncBuffer{}
	script := "trap '' INT; echo stubborn; while true; do sleep 1; done"
	sup := newSupervisor("aux3", "", []string{"sh", "-c", script}, sink)
	sup.grace = 200 * time.Millisecond
	r.NoError(sup.start(context.Background()))

	// Give the shell a moment to install its trap.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(sink.String(), "stubborn") {
		if time.Now().After(deadline) {
			t.Fatal("child never produced output")
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	sup.stop()
	r.Less(time.Since(start), 5*time.Second)
	r.Contains(sink.String(), "aux3 did not exit within")
}

func TestStopAllSilencesExitReports(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := &syncBuffer{}
	first := newSupervisor("one", "", []string{"sh", "-c", "sleep 30"}, sink)
	second := newSupervisor("two", "", []string{"sh", "-c", "sleep 30"}, sink)
	r.NoError(first.start(context.Background()))
	r.NoError(second.start(context.Background()))

	stopAll([]*supervisor{first, second})

	waitDone(t, first)
	waitDone(t, second)
	r.NotContains(sink.String(), "exited on its own")
}
