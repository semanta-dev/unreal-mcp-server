//go:build !windows

package lifecycle

// EnumerateTokenProcesses is Windows-only (production). The non-Windows stub returns
// nil so the daemon builds/tests on CI; the reattach barrier there relies on the
// persisted PID alone.
func EnumerateTokenProcesses(exeSubstr, flag string) []TokenProc { return nil }

// ProcessToken is Windows-only; the stub returns "".
func ProcessToken(pid int, flag string) string { return "" }

// ProcessCommandLine is Windows-only; the stub returns "".
func ProcessCommandLine(pid int) string { return "" }
