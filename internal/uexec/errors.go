package uexec

import "errors"

// Error taxonomy (GO_REWRITE_PLAN.md §11). Callers use errors.Is to branch:
// tool-execution failures become model-visible IsError results; transport faults
// become JSON-RPC errors and drive reconnect.
var (
	// ErrEditorNotFound: discovery found no matching editor node. Not retried —
	// surfaces the actionable "start the editor" message.
	ErrEditorNotFound = errors.New("no Unreal Editor node discovered")

	// ErrConnectionLost: the command connection failed (write/read/EOF/accept
	// exhaustion). Drives a single reconnect attempt at the session layer.
	ErrConnectionLost = errors.New("command connection lost")

	// ErrCommandFailed: the editor reported the command failed (success=false).
	ErrCommandFailed = errors.New("editor reported command failure")

	// ErrProtocol: a malformed or unexpected remote-exec message.
	ErrProtocol = errors.New("malformed remote-exec message")

	// ErrTimeout: a command exceeded its deadline (or ctx was cancelled). The
	// connection is tainted; the next call reconnects. Editor commands are
	// uninterruptible, so the abandoned command may still complete late on a
	// socket we have already closed (see §6.4).
	ErrTimeout = errors.New("editor command timed out")
)
