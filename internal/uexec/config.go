// Package uexec is a faithful Go port of Epic's Python remote-execution wire
// protocol (Engine/.../PythonScriptPlugin remote_execution.py): UDP-multicast
// node discovery + a TCP reverse-connect command channel to a live Unreal
// Editor running the PythonScriptPlugin. Constants mirror the reference client
// exactly so it interoperates with the unmodified editor plugin; any deviation
// is a protocol bug. See GO_REWRITE_PLAN.md §6.
package uexec

import "time"

// Protocol constants (must match remote_execution.py / PythonScriptRemoteExecution.cpp).
const (
	ProtocolVersion = 1
	ProtocolMagic   = "ue_py"

	defaultMulticastGroup = "239.0.0.1:6766"
	defaultBindAddress    = "127.0.0.1"
	defaultCommandAddr    = "127.0.0.1:6776"
	defaultMulticastTTL   = 0

	defaultPingInterval     = 1 * time.Second
	defaultNodeTimeout      = 5 * time.Second
	defaultDiscoveryTimeout = 5 * time.Second
	defaultCommandTimeout   = 120 * time.Second
	defaultAcceptAttempts   = 6
	defaultAcceptTimeout    = 5 * time.Second
)

// MsgType is the protocol message type ("ping", "command", ...).
type MsgType string

const (
	TypePing            MsgType = "ping"
	TypePong            MsgType = "pong"
	TypeOpenConnection  MsgType = "open_connection"
	TypeCloseConnection MsgType = "close_connection"
	TypeCommand         MsgType = "command"
	TypeCommandResult   MsgType = "command_result"
)

// ExecMode selects how the editor runs a command. Values must match the engine
// LexToString for EPythonCommandExecutionMode.
type ExecMode string

const (
	ModeExecFile      ExecMode = "ExecuteFile"       // multi-statement script; returns captured log/output
	ModeExecStatement ExecMode = "ExecuteStatement"  // single statement; prints result
	ModeEval          ExecMode = "EvaluateStatement" // single expression; returns value/repr
)

// Config controls a session's endpoints and timeouts. Use DefaultConfig and
// override fields. Zero-value durations/counts fall back to defaults at use.
type Config struct {
	MulticastGroup string // "239.0.0.1:6766" — discovery group endpoint
	BindAddress    string // "127.0.0.1" — multicast bind + interface
	CommandAddr    string // "127.0.0.1:6776" — TCP reverse-connect listener; ":0" for ephemeral
	MulticastTTL   int    // 0 = localhost only

	// ProjectDir, when set, selects the discovered editor node whose advertised
	// project matches (avoids attaching to the wrong editor on a multi-project box).
	ProjectDir string

	// StrictNode disables the "no match within timeout → first discovered node"
	// fallback in node selection. REQUIRED for a leased per-instance session under the
	// multi-project daemon: during another project's editor cold-start the only node
	// on the shared discovery is a DIFFERENT tenant's editor, and the fallback would
	// pin this session to it (isolation break). With StrictNode, selection returns
	// ErrEditorNotFound instead, so the caller keeps polling for its OWN node.
	StrictNode bool

	PingInterval     time.Duration
	NodeTimeout      time.Duration
	DiscoveryTimeout time.Duration
	CommandTimeout   time.Duration // per-command deadline; 0 = block forever (Python parity)
	AcceptAttempts   int           // reverse-connect accept attempts
	AcceptTimeout    time.Duration // per-attempt accept timeout
}

// DefaultConfig returns a Config matching the reference Python client.
func DefaultConfig() Config {
	return Config{
		MulticastGroup:   defaultMulticastGroup,
		BindAddress:      defaultBindAddress,
		CommandAddr:      defaultCommandAddr,
		MulticastTTL:     defaultMulticastTTL,
		PingInterval:     defaultPingInterval,
		NodeTimeout:      defaultNodeTimeout,
		DiscoveryTimeout: defaultDiscoveryTimeout,
		CommandTimeout:   defaultCommandTimeout,
		AcceptAttempts:   defaultAcceptAttempts,
		AcceptTimeout:    defaultAcceptTimeout,
	}
}

// withDefaults fills any zero fields so tests can set only what they care about.
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.MulticastGroup == "" {
		c.MulticastGroup = d.MulticastGroup
	}
	if c.BindAddress == "" {
		c.BindAddress = d.BindAddress
	}
	if c.CommandAddr == "" {
		c.CommandAddr = d.CommandAddr
	}
	if c.PingInterval == 0 {
		c.PingInterval = d.PingInterval
	}
	if c.NodeTimeout == 0 {
		c.NodeTimeout = d.NodeTimeout
	}
	if c.DiscoveryTimeout == 0 {
		c.DiscoveryTimeout = d.DiscoveryTimeout
	}
	if c.AcceptAttempts == 0 {
		c.AcceptAttempts = d.AcceptAttempts
	}
	if c.AcceptTimeout == 0 {
		c.AcceptTimeout = d.AcceptTimeout
	}
	// CommandTimeout==0 is a legitimate "block forever" value; leave as-is.
	// MulticastTTL==0 is the intended default; leave as-is.
	return c
}
