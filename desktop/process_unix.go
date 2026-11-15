//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// The engine runs in its own process group so one signal reaches it and the
// coding-agent children it started. Without that, quitting the window while an
// agent runs would leave an orphan holding the worktree.
func engineProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func terminateEngine(pid int) error {
	return syscall.Kill(-pid, syscall.SIGTERM)
}

func killEngineTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func processAliveLocal(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
