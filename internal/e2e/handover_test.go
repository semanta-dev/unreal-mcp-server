package e2e

import (
	"context"
	"testing"
)

// Sequential handover on one running editor (plan §2.8): a v1 server was attached,
// then v2 takes over (and later the user rolls back to v1).

// v1 → v2: v2's companion installs into its own module; the resident v1 companion's
// names are untouched and v2 tools work.
func TestHandoverV1ResidentThenV2(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.emu.SetV1Resident(37)
	if res := h.call(t, "editor_status", nil); res.IsError {
		t.Fatalf("v2 must work with a v1 companion resident: %s", text(res))
	}
	if h.emu.V1Version() != 37 {
		t.Fatal("a v2 install must not touch the v1 companion")
	}
	if h.emu.NativeClaims() != 0 {
		t.Fatal("v2 must not claim the native entry point unless the cockpit attaches")
	}
}

// v2 → v1 rollback after v2 claimed the native entry point: the v1 sentinel is
// invalidated, so a reconnecting v1 server reinstalls its own companion instead of
// finding _mcp_dispatch_native redirected to v2.
func TestHandoverClaimInvalidatesV1ForRollback(t *testing.T) {
	h := startHarness(t, harnessOpts{})
	h.emu.SetV1Resident(37)
	if err := h.bridge.ClaimNative(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.emu.NativeClaims() != 1 || h.emu.V1Version() != 0 {
		t.Fatalf("claim must redirect the entry point and invalidate v1 (claims=%d v1=%d)", h.emu.NativeClaims(), h.emu.V1Version())
	}
}
