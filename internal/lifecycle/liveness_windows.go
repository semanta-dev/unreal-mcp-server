//go:build windows

package lifecycle

import (
	"strconv"

	"golang.org/x/sys/windows"
)

const stillActive = 259 // STILL_ACTIVE

// ProcessIdentity returns a stable per-process token — the process CREATION TIME —
// so a recycled PID (Windows reassigns a dead PID to an unrelated process) can be
// detected: the same PID with a different creation time is a different process.
// Returns "" if the process is gone / unqueryable.
func ProcessIdentity(pid int) string {
	if pid <= 0 {
		return ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return ""
	}
	return strconv.FormatInt(creation.Nanoseconds(), 10)
}

// IsAlive reports whether a process with the given PID exists and is running.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
