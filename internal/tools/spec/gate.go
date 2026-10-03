package spec

import "time"

// Gate is the human-approval policy for Destructive/Exec ops (§2.1). The cockpit
// supplies the production implementation; nil means policy "off".
type Gate interface {
	// Required reports whether approval is currently required (policy "require").
	Required() bool
	// Request parks a gated call. The channel yields exactly one decision; sever
	// marks the request abandoned (the agent left) so a late approval is void.
	Request(GateRequest) (decision <-chan Decision, sever func())
}

// GateRequest describes a call awaiting approval.
type GateRequest struct {
	Session string
	Tool    string
	Op      string
	Tier    Tier
	Args    map[string]any
}

// Decision is a human's answer to a GateRequest.
type Decision struct {
	Approved bool
	Reason   string // "denied", "expired", "severed", … when not approved
}

const (
	// MaxSyncApprovalWait bounds how long a synchronous call waits for approval before
	// it returns a pending_approval job instead (never a gate-caused TIMEOUT).
	MaxSyncApprovalWait = 25 * time.Second
	// DefaultGateTimeout bounds the total approval wait (sync + pending job).
	DefaultGateTimeout = 120 * time.Second
	// MaxWait caps a caller's wait_s for async ops.
	MaxWait = 25 * time.Second
)

// DenyGate is the gate for gate_policy "require" when no approval surface is wired:
// every Destructive/Exec op is refused (fail closed) — never silently allowed.
type DenyGate struct{ Reason string }

// Required reports true: the policy is "require".
func (DenyGate) Required() bool { return true }

// Request denies at once.
func (g DenyGate) Request(GateRequest) (<-chan Decision, func()) {
	ch := make(chan Decision, 1)
	ch <- Decision{Approved: false, Reason: g.Reason}
	return ch, func() {}
}

// NoApprovalSurface explains a DenyGate refusal.
const NoApprovalSurface = "gate_policy is \"require\" but no approval surface is available in this server " +
	"version (the cockpit approval UI is not wired to tool calls yet): destructive and exec ops are refused"
