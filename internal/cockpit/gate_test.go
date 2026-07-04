package cockpit

import (
	"encoding/json"
	"testing"
)

func mkGate(id, op string) Gate {
	return Gate{ID: id, OpID: "op-" + id, Op: op, Classification: "destructive", ArgsHash: "h", Diff: json.RawMessage(`{"Mass":{"before":100,"after":0}}`)}
}

func TestGateApproveFlow(t *testing.T) {
	r := NewGateRegistry()
	r.Register(mkGate("g1", "delete_actor"), 1000)
	if len(r.Pending()) != 1 {
		t.Fatal("gate not pending")
	}
	opID, ok := r.Approve("g1")
	if !ok || opID != "op-g1" {
		t.Fatalf("approve = %q,%v", opID, ok)
	}
	if len(r.Pending()) != 0 {
		t.Fatal("approved gate should not be pending")
	}
	// A second approve is a no-op (already decided).
	if _, ok := r.Approve("g1"); ok {
		t.Fatal("double-approve must not succeed")
	}
}

func TestGateSeverBlocksLateApprove(t *testing.T) {
	// The core trust rule: agent departs → sever → a later human Approve is void.
	r := NewGateRegistry()
	r.Register(mkGate("g2", "console"), 1000)
	if !r.Sever("op-g2") {
		t.Fatal("sever should succeed on a pending gate")
	}
	g, _ := r.Get("g2")
	if g.State != GateSevered {
		t.Fatalf("state = %s, want severed", g.State)
	}
	if _, ok := r.Approve("g2"); ok {
		t.Fatal("a SEVERED gate must NOT be approvable (AGENT_SEVERED)")
	}
}

func TestGateSeverAfterApproveIsNoop(t *testing.T) {
	// If the human approved first, a racing sever must not undo it.
	r := NewGateRegistry()
	r.Register(mkGate("g3", "save_all"), 1000)
	if _, ok := r.Approve("g3"); !ok {
		t.Fatal("approve should succeed")
	}
	if r.Sever("op-g3") {
		t.Fatal("sever after approve must be a no-op")
	}
	g, _ := r.Get("g3")
	if g.State != GateApproved {
		t.Fatalf("state = %s, want approved (sever must not override)", g.State)
	}
}

func TestGateDeny(t *testing.T) {
	r := NewGateRegistry()
	r.Register(mkGate("g4", "delete_actor"), 1000)
	opID, ok := r.Deny("g4")
	if !ok || opID != "op-g4" {
		t.Fatalf("deny = %q,%v", opID, ok)
	}
	if _, ok := r.Approve("g4"); ok {
		t.Fatal("a denied gate can't be approved")
	}
}

func TestGateExpire(t *testing.T) {
	r := NewGateRegistry()
	r.Register(mkGate("g5", "console"), 1000)
	// not yet expired
	if exp := r.Expire(1500, 1000); len(exp) != 0 {
		t.Fatalf("premature expiry: %v", exp)
	}
	// past ttl → expired, op released
	exp := r.Expire(2001, 1000)
	if len(exp) != 1 || exp[0] != "op-g5" {
		t.Fatalf("expire = %v", exp)
	}
	if _, ok := r.Approve("g5"); ok {
		t.Fatal("an expired gate can't be approved")
	}
}

func TestGateResolveClearsIndex(t *testing.T) {
	r := NewGateRegistry()
	r.Register(mkGate("g6", "delete_actor"), 1000)
	r.Resolve("g6")
	if _, ok := r.Get("g6"); ok {
		t.Fatal("resolved gate should be gone")
	}
	// op index cleared → a stray sever finds nothing.
	if r.Sever("op-g6") {
		t.Fatal("sever on a resolved op should be a no-op")
	}
}
