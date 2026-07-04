//go:build !windows

package headless

import "os/exec"

// applyCmdLine is a no-op off Windows: UE's BuildFromArgV re-injects value-only
// quotes there, so Go's default argv handling already produces a correct line.
func applyCmdLine(_ *exec.Cmd, _ string, _ []string) {}
