package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	logTailLines  = 50
	logFollowPoll = 250 * time.Millisecond
)

// tailLines returns the last n non-empty-terminated lines of a file.
func tailLines(path string, n int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func followDaemonLog(out io.Writer) error {
	path := daemonLogPath()
	fmt.Fprintf(out, "tailing %s (last %d lines, Ctrl+C to stop)\n", path, logTailLines)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return followFile(ctx, path, out, logTailLines, logFollowPoll)
}

// followFile prints the last initialLines lines, then follows appends. It
// reopens the file when it is rotated or truncated.
func followFile(ctx context.Context, path string, out io.Writer, initialLines int, poll time.Duration) error {
	var file *os.File
	var info os.FileInfo
	var offset int64
	initial := true

	open := func() error {
		handle, err := os.Open(path)
		if err != nil {
			return err
		}
		stat, err := handle.Stat()
		if err != nil {
			handle.Close()
			return err
		}
		file = handle
		info = stat
		offset = 0
		if initial {
			if lines, readErr := tailLines(path, initialLines); readErr == nil {
				for _, line := range lines {
					fmt.Fprintln(out, line)
				}
			}
			initial = false
			offset, _ = handle.Seek(0, io.SeekEnd)
		}
		return nil
	}

	for {
		if file == nil {
			if err := open(); err != nil {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(poll):
				}
				continue
			}
		}

		select {
		case <-ctx.Done():
			file.Close()
			return nil
		default:
		}

		fresh, err := os.Stat(path)
		if err != nil || !os.SameFile(info, fresh) {
			file.Close()
			file = nil
			info = nil
			continue
		}
		if fresh.Size() < offset {
			offset = 0
			_, _ = file.Seek(0, io.SeekStart)
		}

		buf := make([]byte, 32*1024)
		for {
			n, readErr := file.Read(buf)
			if n > 0 {
				if _, writeErr := out.Write(buf[:n]); writeErr != nil {
					file.Close()
					return writeErr
				}
				offset += int64(n)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				break
			}
		}

		select {
		case <-ctx.Done():
			file.Close()
			return nil
		case <-time.After(poll):
		}
	}
}
