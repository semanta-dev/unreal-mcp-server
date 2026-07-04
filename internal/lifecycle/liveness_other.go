//go:build !windows

package lifecycle

import (
	"os"
	"syscall"
)

// IsAlive reports whether a process with the given PID exists (POSIX: signal 0).
// This path exists so the package builds/tests on non-Windows CI; production is Windows.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// ProcessIdentity is the recycled-PID guard token. On non-Windows (CI/build only —
// production is Windows) it returns "" so the pool falls back to liveness-only; the
// recycled-PID race the token defends against is a Windows PID-reuse concern.
func ProcessIdentity(pid int) string { return "" }
