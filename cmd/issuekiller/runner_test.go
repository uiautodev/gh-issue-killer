package main

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOpencodeArgs(t *testing.T) {
	args := opencodeArgs(RunnerOptions{
		Prompt:    "do the thing",
		Model:     "magpie/group/deepseek",
		SessionID: "ses_123",
	})
	for _, want := range []string{"run", "--session", "ses_123", "--model", "magpie/group/deepseek", "--format", "json", "--auto", "do the thing"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if slices.Contains(args, "--agent") {
		t.Fatalf("agent mode must not be passed: %v", args)
	}
}

func TestClaudeArgs(t *testing.T) {
	opts := RunnerOptions{
		Prompt: "plan it",
		Model:  "sonnet",
		RunDir: "/tmp/run",
	}
	args := claudeArgs(opts, "11111111-1111-4111-8111-111111111111")
	for _, want := range []string{"-p", "plan it", "--output-format", "json", "--permission-mode", "bypassPermissions", "--model", "sonnet", "--add-dir", "/tmp/run", "--session-id", "11111111-1111-4111-8111-111111111111"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if slices.Contains(args, "--resume") {
		t.Fatalf("fresh run must not resume: %v", args)
	}

	resumed := claudeArgs(RunnerOptions{Prompt: "fix it", SessionID: "22222222-2222-4222-8222-222222222222"}, "")
	if !slices.Contains(resumed, "--resume") || !slices.Contains(resumed, "22222222-2222-4222-8222-222222222222") {
		t.Fatalf("resume args missing: %v", resumed)
	}
	if slices.Contains(resumed, "--session-id") {
		t.Fatalf("resume must not set a new session id: %v", resumed)
	}
}

func TestCodexArgs(t *testing.T) {
	args := codexArgs(RunnerOptions{
		Cwd:    "/tmp/worktree",
		Prompt: "implement it",
		Model:  "gpt-5.1-codex",
		RunDir: "/tmp/run",
	}, "/tmp/run/last.txt")
	for _, want := range []string{"exec", "implement it", "--cd", "/tmp/worktree", "--full-auto", "--model", "gpt-5.1-codex", "--add-dir", "/tmp/run", "--output-last-message", "/tmp/run/last.txt"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if slices.Contains(args, "--agent") || slices.Contains(args, "--profile") {
		t.Fatalf("agent/profile must not be passed: %v", args)
	}
}

func TestPiArgs(t *testing.T) {
	fresh := piArgs(RunnerOptions{
		Prompt: "do the thing",
		Model:  "howtofix/deepseek-v4.1-flash-1017",
	}, "11111111-1111-4111-8111-111111111111")
	for _, want := range []string{"-p", "--mode", "json", "--model", "howtofix/deepseek-v4.1-flash-1017", "--session-id", "11111111-1111-4111-8111-111111111111", "do the thing"} {
		if !slices.Contains(fresh, want) {
			t.Fatalf("missing %q in %v", want, fresh)
		}
	}
	if slices.Contains(fresh, "--resume") {
		t.Fatalf("pi must not use --resume: %v", fresh)
	}
	if fresh[len(fresh)-1] != "do the thing" {
		t.Fatalf("prompt must be the last argv entry: %v", fresh)
	}

	// Resume reuses the same --session-id flag with the stored id.
	resumed := piArgs(RunnerOptions{Prompt: "fix it", SessionID: "22222222-2222-4222-8222-222222222222"}, "22222222-2222-4222-8222-222222222222")
	if !slices.Contains(resumed, "--session-id") || !slices.Contains(resumed, "22222222-2222-4222-8222-222222222222") {
		t.Fatalf("resume args missing: %v", resumed)
	}
	if slices.Contains(resumed, "--resume") {
		t.Fatalf("resume must not use --resume: %v", resumed)
	}
}

func TestOmpArgs(t *testing.T) {
	fresh := ompArgs(RunnerOptions{
		Prompt: "do the thing",
		Model:  "howtofix/deepseek-v4.1-flash-1017",
	})
	for _, want := range []string{"-p", "--mode", "json", "--model", "howtofix/deepseek-v4.1-flash-1017", "do the thing"} {
		if !slices.Contains(fresh, want) {
			t.Fatalf("missing %q in %v", want, fresh)
		}
	}
	if slices.Contains(fresh, "--resume") || slices.Contains(fresh, "--session-id") {
		t.Fatalf("fresh run must not resume: %v", fresh)
	}
	if fresh[len(fresh)-1] != "do the thing" {
		t.Fatalf("prompt must be the last argv entry: %v", fresh)
	}

	resumed := ompArgs(RunnerOptions{Prompt: "fix it", SessionID: "22222222-2222-4222-8222-222222222222"})
	if !slices.Contains(resumed, "--resume") || !slices.Contains(resumed, "22222222-2222-4222-8222-222222222222") {
		t.Fatalf("resume args missing: %v", resumed)
	}
	if slices.Contains(resumed, "--session-id") {
		t.Fatalf("omp has no --session-id flag: %v", resumed)
	}
}

// Recorded --mode json excerpts (pi 1.5.2 / omp 18.20.3, trimmed to the
// fields the parser reads).
const (
	piSessionLine   = `{"type":"session","version":3,"id":"01a1e38b-65c1-76a3-9905-b87991f8b2b9","timestamp":"2026-11-16T08:48:46.018Z","cwd":"/tmp/pi-probe"}`
	piUserLine      = `{"type":"message_end","message":{"role":"user","content":"Reply with exactly the word: pong"}}`
	piToolUseLine   = `{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_8192a3e46aed40d0a5be1d2f","name":"write","arguments":{"path":"/tmp/pi-live/hello.txt","content":"hi\n"}}],"stopReason":"toolUse"}}`
	piStopLine      = `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Created hello.txt with \"hi\"."}],"stopReason":"stop"}}`
	piErrorLine     = `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"503: {\"message\":\"Server error\",\"type\":\"server_error\",\"code\":\"system_cpu_overloaded\"}"}}`
	piRetryFailLine = `{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"Assistant returned empty stop after retry cap; try switching models or ` + "`/shake images`" + ` to remove archived frames"}`
	piEscapeLine    = "\x1b[?25h\x1b[?2004l\x1b[<u" // pi --resume prints terminal escapes, not JSON
)

func TestPiJSONAccumulator(t *testing.T) {
	acc := &piJSONAccumulator{}
	acc.feed(piSessionLine)
	acc.feed(piUserLine)
	acc.feed(piToolUseLine)
	if acc.sessionID != "01a1e38b-65c1-76a3-9905-b87991f8b2b9" {
		t.Fatalf("sessionID = %q", acc.sessionID)
	}
	if len(acc.texts) != 0 {
		t.Fatalf("user/toolUse messages must add no text: %v", acc.texts)
	}
	if acc.records != 3 {
		t.Fatalf("records = %d, want 3", acc.records)
	}

	// A transient error is superseded by a later good answer.
	acc.feed(piErrorLine)
	if acc.errText == "" {
		t.Fatal("error message must set errText")
	}
	acc.feed(piStopLine)
	if acc.errText != "" {
		t.Fatalf("good answer must clear errText, got %q", acc.errText)
	}
	if len(acc.texts) != 1 || acc.texts[0] != `Created hello.txt with "hi".` {
		t.Fatalf("texts = %v", acc.texts)
	}

	// A failed retry cap is the terminal error.
	acc.feed(piRetryFailLine)
	if acc.errText == "" {
		t.Fatal("failed retry cap must set errText")
	}
}

func TestPiJSONAccumulatorEmptyFailure(t *testing.T) {
	// omp returning empty content: stop with no text, then the retry cap.
	acc := &piJSONAccumulator{}
	acc.feed(piSessionLine)
	acc.feed(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"stop"}}`)
	acc.feed(piRetryFailLine)
	if acc.text() != "" {
		t.Fatalf("text must be empty, got %q", acc.text())
	}
	if acc.errText == "" {
		t.Fatal("empty failure must set errText from finalError")
	}
}

func TestPiJSONAccumulatorSkipsNonJSON(t *testing.T) {
	acc := &piJSONAccumulator{}
	acc.feed(piEscapeLine)
	acc.feed(piSessionLine)
	if acc.sessionID != "01a1e38b-65c1-76a3-9905-b87991f8b2b9" {
		t.Fatalf("sessionID = %q after escape line", acc.sessionID)
	}
	if acc.records != 1 {
		t.Fatalf("records = %d, want 1: a non-JSON line must not count", acc.records)
	}
}

// pi sends a user message's content as a bare string. Decoding content must
// accept it without dropping the record, and the events after it must still
// land.
func TestPiJSONAccumulatorStringContent(t *testing.T) {
	acc := &piJSONAccumulator{}
	acc.feed(piSessionLine)
	acc.feed(piUserLine)
	acc.feed(`{"type":"message_end","message":{"role":"assistant","content":"pong","stopReason":"stop"}}`)
	if acc.text() != "pong" {
		t.Fatalf("text = %q, want pong", acc.text())
	}
	if acc.records != 3 {
		t.Fatalf("records = %d, want 3", acc.records)
	}
	if acc.errText != "" {
		t.Fatalf("errText = %q", acc.errText)
	}

	// An unknown content shape must not reject the record either: the error
	// fields next to it still have to be read.
	acc.feed(`{"type":"message_end","message":{"role":"assistant","content":{"unexpected":true},"stopReason":"error","errorMessage":"boom"}}`)
	if acc.errText != "boom" {
		t.Fatalf("errText = %q, want boom: a record with an unknown content shape must still decode", acc.errText)
	}
}

func TestPiStreamError(t *testing.T) {
	ok := &piJSONAccumulator{records: 2}
	if err := piStreamError("pi", ok, RunnerResult{Text: "done"}); err != nil {
		t.Fatalf("answered run must pass: %v", err)
	}
	// A terse agent that wrote files and said little is not a failed run.
	if err := piStreamError("pi", ok, RunnerResult{}); err != nil {
		t.Fatalf("terse but parsable run must pass: %v", err)
	}
	if err := piStreamError("omp", ok, RunnerResult{ErrText: "429 too many requests"}); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("terminal error must fail the run, got %v", err)
	}

	// No parsable record at all: exit 0 is not success (omp --resume landing on
	// the session picker, or a truncated stream).
	err := piStreamError("omp", &piJSONAccumulator{}, RunnerResult{Code: 0, Stderr: "no session id"})
	if err == nil {
		t.Fatal("a stream with no JSON record must fail the run")
	}
	if !strings.Contains(err.Error(), "without a parsable JSON event") || !strings.Contains(err.Error(), "no session id") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestScanLinesDrainsOverlongRecord(t *testing.T) {
	long := strings.Repeat("a", 400)
	var lines []string
	var truncated []bool
	err := scanLines(strings.NewReader(long+"\n"+piSessionLine+"\ntail"), 256, func(line string, trunc bool) {
		lines = append(lines, line)
		truncated = append(truncated, trunc)
	})
	if err != nil {
		t.Fatalf("scanLines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3: %v", len(lines), lines)
	}
	if lines[0] != long[:256] || !truncated[0] {
		t.Fatalf("first record = %q truncated=%v", lines[0], truncated[0])
	}
	if lines[1] != piSessionLine || truncated[1] {
		t.Fatalf("record after the long one = %q truncated=%v", lines[1], truncated[1])
	}
	if lines[2] != "tail" || truncated[2] {
		t.Fatalf("trailing record without a newline = %q truncated=%v", lines[2], truncated[2])
	}
}

func TestScanLinesReassemblesRecordsAcrossReads(t *testing.T) {
	record := strings.Repeat("b", 200*1024) // larger than scanLines' read buffer
	var lines []string
	err := scanLines(strings.NewReader(record+"\n"), 8<<20, func(line string, trunc bool) {
		if trunc {
			t.Errorf("record was truncated at %d bytes", len(line))
		}
		lines = append(lines, line)
	})
	if err != nil {
		t.Fatalf("scanLines: %v", err)
	}
	if len(lines) != 1 || lines[0] != record {
		t.Fatalf("records = %d, want one %d-byte record", len(lines), len(record))
	}
}

// A record over the 8MB cap must not stop the scan: the child keeps writing
// and the records after the oversized one still arrive.
func TestRunProcessDrainsOverlongRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	script := "head -c 10000000 /dev/zero | tr '\\0' 'a'; printf '\\n'; printf '%s\\n' '" + piSessionLine + "'"
	var lines []string
	proc, err := runProcess(context.Background(), "sh", []string{"-c", script}, RunnerOptions{Timeout: 60 * time.Second}, func(line string) {
		lines = append(lines, line)
	})
	if err != nil {
		t.Fatalf("runProcess: %v", err)
	}
	if proc.Code != 0 {
		t.Fatalf("exit code = %d", proc.Code)
	}
	if len(lines) != 2 {
		t.Fatalf("records = %d, want 2 (the 10MB one and the JSON after it)", len(lines))
	}
	if lines[1] != piSessionLine {
		t.Fatalf("record after the oversized one = %q", lines[1])
	}
}

// A timeout must still kill the agent's children and return promptly: the
// drain grace that covers a lingering grandchild must not delay the stage.
func TestRunProcessTimeoutKillsTheTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	started := time.Now()
	_, err := runProcess(context.Background(), "sh", []string{"-c", "sleep 30 & wait"}, RunnerOptions{Timeout: 300 * time.Millisecond}, nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s: the drain must not wait for a killed grandchild", elapsed)
	}
}

// A CLI that goes silent must be killed well before implementTimeoutMin: the
// stage should block with a stall message instead of sitting at agent:working.
func TestRunProcessStall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	t.Run("silent run is killed", func(t *testing.T) {
		started := time.Now()
		_, err := runProcess(context.Background(), "sh", []string{"-c", "sleep 30 & wait"}, RunnerOptions{
			Timeout:      60 * time.Second,
			StallTimeout: 300 * time.Millisecond,
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "no output for") {
			t.Fatalf("err = %v, want a stall error", err)
		}
		if strings.Contains(err.Error(), "timed out") {
			t.Fatalf("stall was reported as the total timeout: %v", err)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("stall took %s: the drain must not wait for a killed grandchild", elapsed)
		}
	})
	t.Run("steady output is not a stall", func(t *testing.T) {
		script := "for i in 1 2 3 4 5 6 7 8 9 10; do echo tick; sleep 0.1; done"
		var lines []string
		proc, err := runProcess(context.Background(), "sh", []string{"-c", script}, RunnerOptions{
			Timeout:      30 * time.Second,
			StallTimeout: 300 * time.Millisecond,
		}, func(line string) {
			lines = append(lines, line)
		})
		if err != nil {
			t.Fatalf("runProcess: %v", err)
		}
		if proc.Code != 0 {
			t.Fatalf("exit code = %d", proc.Code)
		}
		if len(lines) != 10 {
			t.Fatalf("records = %d, want 10", len(lines))
		}
	})
}

func TestNewCodingRunner(t *testing.T) {
	for _, id := range []string{"opencode", "claude", "codex", "pi", "omp"} {
		runner, err := newCodingRunner(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if runner.ID() != id {
			t.Fatalf("runner id = %q, want %q", runner.ID(), id)
		}
	}
	if _, err := newCodingRunner("vim"); err == nil {
		t.Fatal("expected unsupported runner error")
	}
}
