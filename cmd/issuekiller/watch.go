package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	// Without native file events this is the polling interval. With fsnotify
	// the same value is stretched to a slow safety net (see watchConfig).
	configWatchEvery = 2 * time.Second
	configWatchIdle  = 30 * time.Second
	configDebounce   = 500 * time.Millisecond
)

// watchConfig polls path and fires once it settles on a valid config. Invalid
// edits are logged and ignored until the next change. The returned channel
// receives at most one value.
func watchConfig(ctx context.Context, path string, log *Logger, every, debounce time.Duration) <-chan struct{} {
	ready := make(chan struct{}, 1)
	// Capture the baseline before returning so a write after this call is
	// always seen, even if the goroutine starts late.
	last := configDigest(path)
	events := nativeConfigEvents(ctx, path, log)
	interval := every
	if events != nil {
		// OS notifications drive the reload; the ticker only guards against
		// missed events (network filesystems, watch limits).
		interval = configWatchIdle
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case _, ok := <-events:
				if !ok {
					events = nil
					continue
				}
			}
			digest := configDigest(path)
			if digest == last {
				continue
			}
			// Editors write in several steps; wait for the content to settle.
			time.Sleep(debounce)
			last = configDigest(path)
			if _, err := loadConfig(path); err != nil {
				log.Warn("config changed but is invalid (%v); keeping the running config", err)
				continue
			}
			log.Info("config change detected at %s", path)
			select {
			case ready <- struct{}{}:
			default:
			}
			return
		}
	}()
	return ready
}

func configDigest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "missing"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// nativeConfigEvents watches the config file and its directory with the OS
// notification API (inotify on Linux, kqueue on macOS) through fsnotify. It
// returns nil when the watcher cannot be created, so the caller falls back to
// polling.
func nativeConfigEvents(ctx context.Context, path string, log *Logger) <-chan struct{} {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Debug("config watch unavailable (%v); polling instead", err)
		return nil
	}
	base := filepath.Base(path)
	added := 0
	if err := watcher.Add(filepath.Dir(path)); err == nil {
		added++
	}
	if err := watcher.Add(path); err == nil {
		added++
	}
	if added == 0 {
		_ = watcher.Close()
		return nil
	}
	events := make(chan struct{}, 1)
	go func() {
		defer watcher.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(event.Name) != base {
					continue
				}
				// Editors often replace the file; re-arm the watch on the new
				// inode before reported the change.
				if event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
					_ = watcher.Add(path)
				}
				select {
				case events <- struct{}{}:
				default:
				}
			case err, ok := <-watcher.Errors:
				if ok {
					log.Debug("config watch: %v", err)
				}
			}
		}
	}()
	return events
}
