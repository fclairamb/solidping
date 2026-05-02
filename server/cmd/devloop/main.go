// Package main runs `go build` on file changes and atomically swaps the
// running binary. Replaces `air` for the backend dev loop because air kills
// the running process before building, forcing a multi-second downtime
// window on every save. devloop builds first, then signals the old child to
// exit before starting the new one — the dead window is bounded by graceful
// shutdown, not by build time.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
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
)

var excludeDirs = map[string]struct{}{
	"tmp":      {},
	"vendor":   {},
	".git":     {},
	"testdata": {},
	"res":      {},
	"apps":     {},
	"openapi":  {},
}

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("[devloop] ")

	if err := os.MkdirAll("tmp", 0o755); err != nil {
		log.Fatalf("mkdir tmp: %v", err)
	}

	if err := build(binPath); err != nil {
		log.Fatalf("initial build failed: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	supervisor := newSupervisor()
	if err := supervisor.start(); err != nil {
		log.Fatalf("start child: %v", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("fsnotify: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	if err := addWatches(watcher, rootDir); err != nil {
		log.Fatalf("watch setup: %v", err)
	}
	log.Printf("watching server/ for .go changes")

	rebuilds := make(chan struct{}, 1)
	go debounce(ctx, watcher, rebuilds)

	for {
		select {
		case <-ctx.Done():
			log.Printf("shutting down")
			supervisor.stop()
			return
		case <-rebuilds:
			if err := build(nextBinPath); err != nil {
				log.Printf("build failed (server still running):\n%s", err)
				_ = os.Remove(nextBinPath)
				continue
			}
			supervisor.stop()
			if err := os.Rename(nextBinPath, binPath); err != nil {
				log.Printf("rename: %v", err)
				continue
			}
			if err := supervisor.start(); err != nil {
				log.Printf("start child: %v", err)
			}
		}
	}
}

func build(out string) error {
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Stdout = os.Stdout
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, combined)
	}
	if len(combined) > 0 {
		os.Stdout.Write(combined)
	}
	return nil
}

type supervisor struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func newSupervisor() *supervisor {
	return &supervisor{}
}

func (s *supervisor) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cmd := exec.Command(binPath, binArg)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

func (s *supervisor) stop() {
	s.mu.Lock()
	cmd := s.cmd
	s.cmd = nil
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(graceTimeout):
		log.Printf("child did not exit within %s, killing", graceTimeout)
		_ = cmd.Process.Kill()
		<-done
	}
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
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !shouldRebuild(ev) {
				continue
			}
			if ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() && !isExcludedDir(ev.Name) {
					_ = watcher.Add(ev.Name)
				}
			}
			if timer == nil {
				timer = time.NewTimer(debounceWindow)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounceWindow)
			}
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

func shouldRebuild(ev fsnotify.Event) bool {
	if !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Remove) && !ev.Has(fsnotify.Rename) {
		return false
	}
	name := filepath.Base(ev.Name)
	if !strings.HasSuffix(name, ".go") {
		return false
	}
	if strings.HasSuffix(name, "_test.go") {
		return false
	}
	return !isExcludedDir(filepath.Dir(ev.Name))
}

func addWatches(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if isExcludedDir(path) {
			return filepath.SkipDir
		}
		return watcher.Add(path)
	})
}

func isExcludedDir(path string) bool {
	clean := filepath.Clean(path)
	parts := strings.Split(clean, string(filepath.Separator))
	for _, p := range parts {
		if _, ok := excludeDirs[p]; ok {
			return true
		}
	}
	return false
}
