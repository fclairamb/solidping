// Package main runs `go build` on file changes and atomically swaps the
// running binary. Replaces `air` for the backend dev loop because air kills
// the running process before building, forcing a multi-second downtime
// window on every save. devloop builds first, then signals the old child to
// exit before starting the new one — the dead window is bounded by graceful
// shutdown, not by build time.
//
// devloop is also the dev-time process supervisor: auxiliary children given
// via -proc (the dash0/status0 `bun run dev` servers) run as children of this
// foreground process and are reaped on shutdown, and every supervised stream
// (devloop's own lines, build output, each child's combined stdout/stderr)
// is mirrored to the terminal and to a size-rotated <name>.log under -log-dir.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	debounceWindow = 300 * time.Millisecond
	graceTimeout   = 2 * time.Second
	binPath        = "./tmp/solidping"
	nextBinPath    = "./tmp/solidping.next"
	binArg         = "serve"
	rootDir        = "."
	backendName    = "backend"
)

// config carries the parsed command-line flags.
type config struct {
	logDir     string
	logMaxSize int64
	logBackups int
	procs      []procSpec
}

func excludedDirNames() map[string]struct{} {
	return map[string]struct{}{
		"tmp":      {},
		"vendor":   {},
		".git":     {},
		"testdata": {},
		"res":      {},
		"apps":     {},
		"openapi":  {},
	}
}

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("[devloop] ")

	cfg, err := parseFlags()
	if err != nil {
		log.Printf("%v", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	err = run(ctx, cfg)
	cancel()
	if err != nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}

func parseFlags() (config, error) {
	var procs stringList
	logDir := flag.String("log-dir", "", "directory for size-rotated log files; empty disables file logging")
	logMaxSize := flag.Int64("log-max-size", defaultLogMaxSize, "max active log file size in bytes before rotation")
	logBackups := flag.Int("log-backups", defaultLogBackups, "rotated backup files to keep per log")
	flag.Var(&procs, "proc", "auxiliary process to supervise, as name:dir:command (repeatable)")
	flag.Parse()

	specs, err := parseProcSpecs(procs)
	if err != nil {
		return config{}, err
	}
	return config{logDir: *logDir, logMaxSize: *logMaxSize, logBackups: *logBackups, procs: specs}, nil
}

func run(ctx context.Context, cfg config) error {
	sink := newSink(cfg.logDir, backendName, cfg.logMaxSize, cfg.logBackups)
	log.SetOutput(sink)

	if err := os.MkdirAll("tmp", 0o755); err != nil {
		return fmt.Errorf("mkdir tmp: %w", err)
	}

	if err := build(ctx, binPath, sink); err != nil {
		return fmt.Errorf("initial build failed: %w", err)
	}

	backend := newSupervisor(backendName, "", []string{binPath, binArg}, sink)
	if err := backend.start(ctx); err != nil {
		return fmt.Errorf("start child: %w", err)
	}
	children := startAuxProcs(ctx, cfg, backend)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	if err := addWatches(watcher, rootDir); err != nil {
		return fmt.Errorf("watch setup: %w", err)
	}
	log.Printf("watching server/ for .go and .html changes")

	rebuilds := make(chan struct{}, 1)
	go debounce(ctx, watcher, rebuilds)

	for {
		select {
		case <-ctx.Done():
			log.Printf("shutting down")
			stopAll(children)
			return nil
		case <-rebuilds:
			handleRebuild(ctx, backend, sink)
		}
	}
}

// startAuxProcs launches the -proc children, each with its own rotating log
// sink. A child that fails to start is reported loudly but does not abort the
// loop (parity with the old backgrounded `bun run dev &` pipelines).
func startAuxProcs(ctx context.Context, cfg config, backend *supervisor) []*supervisor {
	children := make([]*supervisor, 0, len(cfg.procs)+1)
	children = append(children, backend)
	for i := range cfg.procs {
		spec := &cfg.procs[i]
		child := newSupervisor(spec.name, spec.dir, spec.argv, newSink(cfg.logDir, spec.name, cfg.logMaxSize, cfg.logBackups))
		sinkLogf(child.sink, "supervising %s: %s (dir %s)", spec.name, strings.Join(spec.argv, " "), spec.dir)
		if err := child.start(ctx); err != nil {
			sinkLogf(child.sink, "start %s failed: %v", spec.name, err)
			continue
		}
		children = append(children, child)
	}
	return children
}

func handleRebuild(ctx context.Context, backend *supervisor, sink io.Writer) {
	if err := build(ctx, nextBinPath, sink); err != nil {
		log.Printf("build failed (server still running):\n%v", err)
		_ = os.Remove(nextBinPath)
		return
	}
	backend.stop()
	if err := os.Rename(nextBinPath, binPath); err != nil {
		log.Printf("rename: %v", err)
		return
	}
	if err := backend.start(ctx); err != nil {
		log.Printf("start child: %v", err)
	}
}

func build(ctx context.Context, out string, sink io.Writer) error {
	// -s -w strips debug symbols; cuts per-rebuild link time from ~6.5 s to ~2 s with no
	// effect on runtime behavior. dlv attaches to source, not the binary symbols.
	cmd := exec.CommandContext(ctx, "go", "build", "-ldflags", "-s -w", "-o", out, ".")
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, combined)
	}
	if len(combined) > 0 {
		if _, werr := sink.Write(combined); werr != nil {
			return fmt.Errorf("write build output: %w", werr)
		}
	}
	return nil
}

func debounce(ctx context.Context, watcher *fsnotify.Watcher, out chan<- struct{}) {
	var (
		timer  *time.Timer
		timerC <-chan time.Time
	)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			handleEvent(watcher, event)
			if !shouldRebuild(event) {
				continue
			}
			timer, timerC = resetTimer(timer)
		case <-timerC:
			timer = nil
			timerC = nil
			select {
			case out <- struct{}{}:
			default:
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

func handleEvent(watcher *fsnotify.Watcher, event fsnotify.Event) {
	if !event.Has(fsnotify.Create) {
		return
	}
	info, err := os.Stat(event.Name)
	if err != nil || !info.IsDir() || isExcludedDir(event.Name) {
		return
	}
	_ = watcher.Add(event.Name)
}

func resetTimer(timer *time.Timer) (*time.Timer, <-chan time.Time) {
	if timer == nil {
		newTimer := time.NewTimer(debounceWindow)
		return newTimer, newTimer.C
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(debounceWindow)
	return timer, timer.C
}

func shouldRebuild(event fsnotify.Event) bool {
	const watchedOps = fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename
	if event.Op&watchedOps == 0 {
		return false
	}
	name := filepath.Base(event.Name)
	// .html as well as .go: the email templates in internal/email/templates are
	// //go:embed-ed into the binary, so editing one changes nothing until a
	// rebuild — which made the /api/mgmt/email-preview loop (whose entire point
	// is iterating on those templates) silently serve the previous version.
	if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".html") {
		return false
	}
	if strings.HasSuffix(name, "_test.go") {
		return false
	}
	return !isExcludedDir(filepath.Dir(event.Name))
}

func addWatches(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, dirEntry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return err
		}
		if !dirEntry.IsDir() {
			return nil
		}
		if isExcludedDir(path) {
			return filepath.SkipDir
		}
		return watcher.Add(path)
	})
}

func isExcludedDir(path string) bool {
	excluded := excludedDirNames()
	clean := filepath.Clean(path)
	parts := strings.Split(clean, string(filepath.Separator))
	for _, p := range parts {
		if _, ok := excluded[p]; ok {
			return true
		}
	}
	return false
}
