package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

func TestShouldRebuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event fsnotify.Event
		want  bool
	}{
		{name: "go write", event: fsnotify.Event{Name: "internal/app/server.go", Op: fsnotify.Write}, want: true},
		{name: "html create", event: fsnotify.Event{Name: "internal/email/a.html", Op: fsnotify.Create}, want: true},
		{name: "chmod only", event: fsnotify.Event{Name: "main.go", Op: fsnotify.Chmod}, want: false},
		{name: "other extension", event: fsnotify.Event{Name: "README.md", Op: fsnotify.Write}, want: false},
		{name: "test file", event: fsnotify.Event{Name: "main_test.go", Op: fsnotify.Write}, want: false},
		{name: "excluded dir", event: fsnotify.Event{Name: "vendor/x/y.go", Op: fsnotify.Remove}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, shouldRebuild(tt.event))
		})
	}
}

func TestIsExcludedDir(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(isExcludedDir("./tmp"))
	r.True(isExcludedDir("internal/testdata/x"))
	r.True(isExcludedDir(".git/objects"))
	r.False(isExcludedDir("internal/app"))
	r.False(isExcludedDir("."))
}

func TestResetTimer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	timer, c := resetTimer(nil)
	r.NotNil(timer)
	r.NotNil(c)

	// Reset while armed.
	again, c2 := resetTimer(timer)
	r.Same(timer, again)
	r.NotNil(c2)

	// Reset after it fired without the channel being drained.
	time.Sleep(debounceWindow + 50*time.Millisecond)
	again, _ = resetTimer(timer)
	r.Same(timer, again)
	again.Stop()
}

func TestNewSink(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(os.Stdout, newSink("", "backend", 1024, 1))

	dir := filepath.Join(t.TempDir(), "logs")
	sink := newSink(dir, "backend", 1024, 1)
	_, err := sink.Write([]byte("hello\n"))
	r.NoError(err)
	data, err := os.ReadFile(filepath.Join(dir, "backend.log"))
	r.NoError(err)
	r.Contains(string(data), "hello")

	// A log dir that cannot be created falls back to the terminal.
	blocker := filepath.Join(t.TempDir(), "file")
	r.NoError(os.WriteFile(blocker, []byte("x"), 0o600))
	r.Equal(os.Stdout, newSink(filepath.Join(blocker, "sub"), "backend", 1024, 1))
}

func TestAddWatchesAndHandleEvent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	root := t.TempDir()
	r.NoError(os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755))
	r.NoError(os.MkdirAll(filepath.Join(root, "vendor", "dep"), 0o755))
	r.NoError(os.WriteFile(filepath.Join(root, "pkg", "a.go"), []byte("package pkg\n"), 0o600))

	watcher, err := fsnotify.NewWatcher()
	r.NoError(err)
	defer func() { _ = watcher.Close() }()

	r.NoError(addWatches(watcher, root))
	list := watcher.WatchList()
	r.Contains(list, filepath.Join(root, "pkg", "sub"))
	for _, p := range list {
		r.NotContains(p, "vendor")
	}

	// A missing root is reported.
	r.Error(addWatches(watcher, filepath.Join(root, "missing")))

	// A created directory gets watched; files, excluded dirs and other ops do not.
	newDir := filepath.Join(root, "pkg", "fresh")
	r.NoError(os.Mkdir(newDir, 0o755))
	handleEvent(watcher, fsnotify.Event{Name: newDir, Op: fsnotify.Create})
	r.Contains(watcher.WatchList(), newDir)

	handleEvent(watcher, fsnotify.Event{Name: filepath.Join(root, "pkg", "a.go"), Op: fsnotify.Create})
	handleEvent(watcher, fsnotify.Event{Name: filepath.Join(root, "vendor", "dep"), Op: fsnotify.Create})
	handleEvent(watcher, fsnotify.Event{Name: filepath.Join(root, "gone"), Op: fsnotify.Create})
	handleEvent(watcher, fsnotify.Event{Name: newDir, Op: fsnotify.Write})
	r.NotContains(watcher.WatchList(), filepath.Join(root, "vendor", "dep"))
}

func TestDebounce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	root := t.TempDir()
	watcher, err := fsnotify.NewWatcher()
	r.NoError(err)
	r.NoError(watcher.Add(root))

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		debounce(ctx, watcher, out)
		close(done)
	}()

	// Two quick writes collapse into one rebuild signal.
	r.NoError(os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600))
	r.NoError(os.WriteFile(filepath.Join(root, "b.txt"), []byte("ignored"), 0o600))
	r.NoError(os.WriteFile(filepath.Join(root, "a.go"), []byte("package a // x\n"), 0o600))

	select {
	case <-out:
	case <-time.After(5 * time.Second):
		r.Fail("no rebuild signal")
	}

	cancel()
	<-done

	// A closed watcher ends the loop too.
	watcher2, err := fsnotify.NewWatcher()
	r.NoError(err)
	r.NoError(watcher2.Close())
	done2 := make(chan struct{})
	go func() {
		debounce(context.Background(), watcher2, out)
		close(done2)
	}()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		r.Fail("debounce did not stop on a closed watcher")
	}
	_ = watcher.Close()
}

func TestBuildFailureKeepsServer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := &syncBuffer{}

	err := build(ctx, filepath.Join(t.TempDir(), "out"), sink)
	r.Error(err)

	// A failed rebuild leaves the backend untouched.
	backend := newSupervisor("backend", "", []string{"true"}, sink)
	handleRebuild(ctx, backend, sink)
	r.Nil(backend.cmd)
}

func TestStartAuxProcs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	logDir := t.TempDir()
	cfg := config{
		logDir:     logDir,
		logMaxSize: 1 << 20,
		logBackups: 1,
		procs: []procSpec{
			{name: "ok", dir: t.TempDir(), argv: []string{"sleep", "30"}},
			{name: "broken", dir: t.TempDir(), argv: []string{"/nonexistent/devloop-binary"}},
		},
	}
	backend := newSupervisor("backend", "", []string{"sleep", "30"}, &syncBuffer{})

	children := startAuxProcs(context.Background(), cfg, backend)
	r.Len(children, 2)
	stopAll(children[1:])

	data, err := os.ReadFile(filepath.Join(logDir, "broken.log"))
	r.NoError(err)
	r.Contains(string(data), "start broken failed")
}

// TestRunBuildsWatchesAndRebuilds drives the whole loop on a throwaway
// module: initial build, child start, a source edit that triggers a rebuild
// and swap, then a clean shutdown on context cancel.
func TestRunBuildsWatchesAndRebuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go module")
	}
	r := require.New(t)

	root := t.TempDir()
	r.NoError(os.WriteFile(filepath.Join(root, "go.mod"), []byte("module devlooptest\n\ngo 1.21\n"), 0o600))
	mainSrc := "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(30 * time.Second) }\n"
	r.NoError(os.WriteFile(filepath.Join(root, "main.go"), []byte(mainSrc), 0o600))
	t.Chdir(root)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	logDir := filepath.Join(root, "logs")
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, config{logDir: logDir, logMaxSize: 1 << 20, logBackups: 1}) }()

	waitFor := func(cond func() bool) {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		r.Fail("condition not met in time")
	}
	backendLog := func() string {
		data, _ := os.ReadFile(filepath.Join(logDir, "backend.log"))
		return string(data)
	}

	waitFor(func() bool { return strings.Contains(backendLog(), "watching server/") })
	binInfo, err := os.Stat(binPath)
	r.NoError(err)

	r.NoError(os.WriteFile(filepath.Join(root, "main.go"), []byte(mainSrc+"\n// edit\n"), 0o600))
	waitFor(func() bool {
		info, statErr := os.Stat(binPath)
		return statErr == nil && info.ModTime().After(binInfo.ModTime())
	})

	cancel()
	select {
	case err := <-errCh:
		r.NoError(err)
	case <-time.After(30 * time.Second):
		r.Fail("run did not return after cancel")
	}
	r.Contains(backendLog(), "shutting down")
}
