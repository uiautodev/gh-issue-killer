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
	color := logsColorEnabled(out)
	fmt.Fprintf(out, "%s\n", colorizeHeader(fmt.Sprintf("tailing %s (last %d lines, Ctrl+C to stop)", path, logTailLines), color))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	writer := &colorLineWriter{out: out, color: color}
	err := followFile(ctx, path, writer, logTailLines, logFollowPoll)
	_ = writer.Flush()
	return err
}

func logsColorEnabled(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	file, ok := out.(*os.File)
	return ok && isTerminal(file)
}

type colorLineWriter struct {
	out     io.Writer
	color   bool
	pending string
}

func (w *colorLineWriter) Write(p []byte) (int, error) {
	w.pending += string(p)
	for {
		index := strings.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		line := w.pending[:index]
		w.pending = w.pending[index+1:]
		if _, err := fmt.Fprintln(w.out, colorizeLogLine(line, w.color)); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *colorLineWriter) Flush() error {
	if w.pending == "" {
		return nil
	}
	line := w.pending
	w.pending = ""
	_, err := fmt.Fprintln(w.out, colorizeLogLine(line, w.color))
	return err
}

const (
	colorReset  = "\x1b[0m"
	colorDim    = "\x1b[2m"
	colorRed    = "\x1b[31m"
	colorGreen  = "\x1b[32m"
	colorCyan   = "\x1b[36m"
	colorGray   = "\x1b[90m"
	colorYellow = "\x1b[33m"
)

func colorizeHeader(text string, color bool) string {
	if !color {
		return text
	}
	return colorCyan + text + colorReset
}

// colorizeLogLine colors the timestamp and level in a line produced by
// Logger. The log file itself is never modified.
func colorizeLogLine(line string, color bool) string {
	if !color {
		return line
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return line
	}
	level := strings.TrimSpace(parts[1])
	levelColor := ""
	switch level {
	case "DEBUG":
		levelColor = colorGray
	case "INFO":
		levelColor = colorGreen
	case "WARN":
		levelColor = colorYellow
	case "ERROR":
		levelColor = colorRed
	default:
		return line
	}
	return colorDim + parts[0] + colorReset + " " + levelColor + level + colorReset + " " + parts[2]
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
