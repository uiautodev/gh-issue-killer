package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"time"
)

const (
	configWatchEvery = 2 * time.Second
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
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
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
