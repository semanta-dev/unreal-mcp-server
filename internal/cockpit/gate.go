package cockpit

import (
	"encoding/json"
	"sync"
	"time"
)

// GateState is the lifecycle of an op parked pending human approval (EDITOR_PLUGIN_PLAN.md
// §4.2). The crux is Severed: if the agent's tool call departs (client disconnect / ctx
// cancel) while a gate is parked, a later human Approve must NOT execute the op — it
// resolves AGENT_SEVERED instead, so an abandoned gate is never silently run.
type GateState string

const (
	GatePending  GateState = "pending"
	GateApproved GateState = "approved"
	GateDenied   GateState = "denied"
	GateSevered  GateState = "severed" // agent gone before decision → a late approve is void
	GateExpired  GateState = "expired" // timed out
)

// Gate is one parked op awaiting a human decision. Diff is the before→after of the target
// so the human inspects an actual value change, not a blind args hash (§4.2, gamedev fix).
type Gate struct {
	ID             string          `json:"gate_id"`
	OpID           string          `json:"op_id"`
	Op             string          `json:"op"`
	Classification string          `json:"classification"`
	ArgsHash       string          `json:"args_hash"`
	Diff           json.RawMessage `json:"diff"`
	State          GateState       `json:"state"`
	CreatedUnixMs  int64           `json:"created_ms"`
}

// GateRegistry tracks parked gates for the cockpit and enforces the sever rule. It is the
// Go-side authority; the editor still physically parks the op, but the registry decides
// whether a human Approve is honored or rejected as AGENT_SEVERED.
type GateRegistry struct {
	mu      sync.Mutex
	gates   map[string]*Gate  // by gate_id
	byOp    map[string]string // op_id -> gate_id
	severed map[string]bool   // op_id tombstones: a sever that arrived before/without a gate
}

// NewGateRegistry builds an empty registry.
func NewGateRegistry() *GateRegistry {
	return &GateRegistry{gates: map[string]*Gate{}, byOp: map[string]string{}, severed: map[string]bool{}}
}

// Register records a newly-parked gate. nowMs is passed in so the registry stays
// deterministic in tests. Two safety rules: a duplicate gate_id is ignored (never clobbers
// a decided gate), and if the op was already SEVERED (the agent departed before the gate
// frame arrived — a cross-goroutine ordering hole), the gate lands as GateSevered, NOT
// pending, so a later human approve can never run a departed agent's op (§4.2).
func (r *GateRegistry) Register(g Gate, nowMs int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.gates[g.ID]; exists {
		return // duplicate gate_id — ignore (GUIDs shouldn't collide; never resurrect a decision)
	}
	g.CreatedUnixMs = nowMs
	if r.severed[g.OpID] {
		g.State = GateSevered
	} else {
		g.State = GatePending
	}
	gc := g
	r.gates[g.ID] = &gc
	r.byOp[g.OpID] = g.ID
}

// Get returns a copy of the gate.
func (r *GateRegistry) Get(gateID string) (Gate, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gates[gateID]; ok {
		return *g, true
	}
	return Gate{}, false
}

// Pending returns copies of all still-pending gates (for the cockpit gate list).
func (r *GateRegistry) Pending() []Gate {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Gate
	for _, g := range r.gates {
		if g.State == GatePending {
			out = append(out, *g)
		}
	}
	return out
}

// Approve honors a human approval. It returns the op_id to release AND ok=true only if the
// gate is still pending. A missing gate → ok=false; a SEVERED gate → ok=false (the caller
// surfaces AGENT_SEVERED and does NOT forward the approve to the editor).
func (r *GateRegistry) Approve(gateID string) (opID string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, exists := r.gates[gateID]
	if !exists || g.State != GatePending {
		return "", false
	}
	g.State = GateApproved
	return g.OpID, true
}

// Deny marks a gate denied and returns its op_id (the editor drops the parked op).
func (r *GateRegistry) Deny(gateID string) (opID string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, exists := r.gates[gateID]
	if !exists || g.State != GatePending {
		return "", false
	}
	g.State = GateDenied
	return g.OpID, true
}

// Sever marks the op severed — called when the agent's tool call departs (ctx cancel /
// disconnect). It records a DURABLE tombstone so it works even if it races AHEAD of the
// gate frame (Register then lands the gate as severed). If the gate already exists and is
// pending, it is transitioned to severed immediately. A subsequent Approve returns ok=false.
func (r *GateRegistry) Sever(opID string) (severed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.severed[opID] = true // durable — survives a later Register (cleared on terminal result)
	gateID, ok := r.byOp[opID]
	if !ok {
		return true // no gate yet; the tombstone will catch a later Register
	}
	g, exists := r.gates[gateID]
	if !exists || g.State != GatePending {
		return false
	}
	g.State = GateSevered
	return true
}

// Expire marks any pending gate older than ttl as expired and returns their op_ids. The
// GateTimeout must be a documented fraction of the MCP call timeout (§4.2, eng10x fix), so
// the caller sweeps periodically and resolves the parked calls before the client gives up.
func (r *GateRegistry) Expire(nowMs int64, ttlMs int64) (expired []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range r.gates {
		if g.State == GatePending && nowMs-g.CreatedUnixMs >= ttlMs {
			g.State = GateExpired
			expired = append(expired, g.OpID)
		}
	}
	return expired
}

// Resolve removes a gate (by gate_id) once its op has reached a terminal result, and
// clears the op's tombstone.
func (r *GateRegistry) Resolve(gateID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gates[gateID]; ok {
		delete(r.byOp, g.OpID)
		delete(r.gates, gateID)
		delete(r.severed, g.OpID)
	}
}

// ResolveByOp removes any gate for opID and clears its tombstone — the feedback edge from
// a terminal rpc_result (§4.2), so the registry never grows unbounded over a long session.
// Called for EVERY rpc_result (even a departed agent's DENIED), so a severed tombstone is
// always eventually cleared.
func (r *GateRegistry) ResolveByOp(opID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if gateID, ok := r.byOp[opID]; ok {
		delete(r.gates, gateID)
		delete(r.byOp, opID)
	}
	delete(r.severed, opID)
}

// revert returns a just-Approved/Denied gate to Pending — a compensation when the control
// forward to the editor failed, so the human can retry (unless the op was severed meanwhile).
func (r *GateRegistry) revert(gateID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.gates[gateID]
	if !ok || r.severed[g.OpID] {
		return
	}
	if g.State == GateApproved || g.State == GateDenied {
		g.State = GatePending
	}
}

// nowMs is a convenience for callers that do have a clock (not used inside the registry).
func nowMs(t time.Time) int64 { return t.UnixMilli() }
