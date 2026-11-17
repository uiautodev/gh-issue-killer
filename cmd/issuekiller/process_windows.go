//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// Windows has no signals, no session ids and no process groups as POSIX knows
// them. These helpers keep the daemon, the agent runner and the desktop shell
// working with taskkill / CREATE_NEW_PROCESS_GROUP instead.

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
	stillActive           = 259
)

// processAlive uses OpenProcess + GetExitCodeProcess: os.FindProcess always
// succeeds on Windows and signal 0 does not exist.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

func terminateProcess(pid int) error {
	if !processAlive(pid) {
		return os.ErrProcessDone
	}
	// taskkill /T also ends the agent children the engine started.
	if err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run(); err != nil {
		proc, findErr := os.FindProcess(pid)
		if findErr != nil {
			return findErr
		}
		return proc.Kill()
	}
	return nil
}

func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
		HideWindow:    true,
	}
}

func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup,
		HideWindow:    true,
	}
}

func killChildTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

func shellCommand(command string) (string, []string) {
	return "cmd", []string{"/C", command}
}

// canExecInPlace is false on Windows: reexec starts a new process and lets the
// old one exit (or, under the desktop shell, exits and is restarted).
const canExecInPlace = false

func execInPlace(exe string, args, env []string) error {
	return os.ErrInvalid
}
