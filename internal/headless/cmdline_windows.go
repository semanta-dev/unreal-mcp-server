//go:build windows

package headless

import (
	"os/exec"
	"syscall"
)

// applyCmdLine overrides the raw command line with UE-correct value-only quoting,
// bypassing Go's EscapeArg (which would whole-quote "-ExecCmds=..." and break
// UE's FParse on Windows).
func applyCmdLine(cmd *exec.Cmd, exe string, args []string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: buildWindowsCmdLine(exe, args)}
}
