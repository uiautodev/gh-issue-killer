//go:build !windows

package main

import (
	"errors"
	"path/filepath"
	"syscall"
)

// The daemon, the coding agents and the desktop shell all need to start and
// stop other processes. POSIX signals and process groups do not exist on
// Windows, so every call site goes through these helpers instead of syscall.

// processAlive reports whether pid is a live process we are allowed to signal.
// EPERM means the process exists but belongs to another user, which still
// counts: the lock file must not be stolen from it.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// terminateProcess asks pid to exit. Callers poll processAlive afterwards and
// report a timeout instead of escalating, so running jobs can finish.
func terminateProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// detachSysProcAttr puts the background daemon in its own session so it
// survives the terminal that started it.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// childSysProcAttr gives a child its own process group, so a timeout can kill
// the agent CLI together with every tool it spawned.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func killChildTree(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

func shellCommand(command string) (string, []string) {
	return "/bin/sh", []string{"-c", command}
}

// canExecInPlace reports whether reexec can replace the process image while
// keeping the pid (and the desktop shell's child handle) valid.
const canExecInPlace = true

func execInPlace(exe string, args, env []string) error {
	return syscall.Exec(exe, args, env)
}

// ghAgentWrapperFile is the two-line wrapper around gh with the bot config
// dir. Windows uses a .cmd file instead of a shell script.
func ghAgentWrapperFile() (string, string) {
	return filepath.Join(homeDir(), ".local", "bin", "gh-agent"),
		"#!/bin/sh\nGH_CONFIG_DIR=\"$HOME/.config/gh-agent\" exec gh \"$@\"\n"
}
