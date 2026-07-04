// Package headless builds and runs UnrealEditor-Cmd invocations for work that
// doesn't need an interactive editor: commandlets (cook/resave/validate),
// -ExecCmds batches, and the automation test runner. An autonomous agent uses
// this to run a check or a batch job WITHOUT tying up (or waiting on) the
// interactive editor that holds the live command channel — the P6 fan-out path.
// The argument construction is pure and unit-tested; Run adds process exec.
package headless

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Cmd describes a headless run. Editor is the UnrealEditor-Cmd path; Project is
// the .uproject. Exactly one of Commandlet / ExecCmds / RunTests drives the mode.
type Cmd struct {
	Editor     string   // path to UnrealEditor-Cmd(.exe); if empty, EditorCmdName() is used
	Project    string   // path to the .uproject
	Commandlet string   // -run=<Commandlet> (e.g. ResavePackages, DataValidation)
	ExecCmds   []string // -ExecCmds="a;b;Quit"
	RunTests   string   // automation filter; expands to Automation RunTests <filter>
	NullRHI    bool     // -nullrhi (no GPU) — default true for pure logic jobs
	Unattended bool     // -unattended -nopause -nosplash (default true)
	Extra      []string // extra raw flags
}

// EditorCmdName is the platform editor-cmd binary name.
func EditorCmdName() string {
	if runtime.GOOS == "windows" {
		return "UnrealEditor-Cmd.exe"
	}
	return "UnrealEditor-Cmd"
}

// Args returns the full argument vector (excluding the executable) for the run.
func (c Cmd) Args() []string {
	args := []string{c.Project}
	if c.Commandlet != "" {
		args = append(args, "-run="+c.Commandlet)
	}
	execs := append([]string(nil), c.ExecCmds...)
	if c.RunTests != "" {
		// Automation RunTests is ASYNCHRONOUS — appending Quit to the same batch
		// quits before the tests finish. -TestExit waits for the queue to drain,
		// then exits with the correct code.
		execs = append(execs, "Automation RunTests "+c.RunTests)
		args = append(args, `-TestExit=Automation Test Queue Empty`)
	}
	if len(execs) > 0 {
		args = append(args, `-ExecCmds=`+strings.Join(execs, ";"))
	}
	if c.NullRHI {
		args = append(args, "-nullrhi")
	}
	if c.Unattended {
		args = append(args, "-unattended", "-nopause", "-nosplash", "-nullhmd")
	}
	args = append(args, "-stdout", "-utf8output", "-nop4")
	args = append(args, c.Extra...)
	return args
}

// Executable returns the editor-cmd path to run.
func (c Cmd) Executable() string {
	if c.Editor != "" {
		return c.Editor
	}
	return EditorCmdName()
}

// Result is the outcome of a headless run.
type Result struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timed_out"`
	Cmdline  string `json:"cmdline"`
}

// Run executes the headless command, capturing stdout/stderr. Cancel ctx (e.g. a
// timeout) to abort; TimedOut is set when the context ended the run.
func Run(ctx context.Context, c Cmd) (Result, error) {
	exe := c.Executable()
	args := c.Args()
	cmd := exec.CommandContext(ctx, exe, args...)
	// On Windows, override the raw command line: Go's default EscapeArg wraps a
	// space-containing arg wholly in quotes ("-ExecCmds=..."), but UE parses the
	// raw GetCommandLine() and FParse::Value only honors a value quote placed
	// immediately after '=' — so it would read only the first token ("Automation")
	// and quit before tests run. applyCmdLine is a no-op on POSIX (default handling
	// is correct there).
	applyCmdLine(cmd, exe, args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Cmdline: exe + " " + strings.Join(args, " "),
	}
	if ctx.Err() == context.DeadlineExceeded || ctx.Err() == context.Canceled {
		res.TimedOut = true
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil && res.ExitCode == 0 && !res.TimedOut {
		res.ExitCode = -1
	}
	return res, err
}

// EditorCmdFromEngineDir derives the editor-cmd path from an engine directory.
func EditorCmdFromEngineDir(engineDir string) string {
	if engineDir == "" {
		return EditorCmdName()
	}
	return filepath.Join(engineDir, "Engine", "Binaries", platformDir(), EditorCmdName())
}

func platformDir() string {
	switch runtime.GOOS {
	case "windows":
		return "Win64"
	case "darwin":
		return "Mac"
	default:
		return "Linux"
	}
}

// buildWindowsCmdLine reconstructs the raw Windows command line the way UE's
// FParse expects: for a "-Flag=value with spaces" arg the quote goes immediately
// after '=' (-Flag="value with spaces"), NOT around the whole token; a bare arg
// with spaces (the .uproject path) is whole-quoted. Pure + unit-tested; the
// Windows-only applyCmdLine feeds this into SysProcAttr.CmdLine.
func buildWindowsCmdLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteWindowsArg(exe))
	for _, a := range args {
		parts = append(parts, quoteWindowsArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteWindowsArg(a string) string {
	if i := strings.IndexByte(a, '='); i >= 0 {
		flag, val := a[:i+1], a[i+1:]
		if strings.ContainsAny(val, " \t") {
			return flag + `"` + val + `"` // value quoted right after '='
		}
		return a
	}
	if strings.ContainsAny(a, " \t") {
		return `"` + a + `"`
	}
	return a
}
