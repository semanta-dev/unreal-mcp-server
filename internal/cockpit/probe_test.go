package cockpit

import (
	"encoding/json"
	"testing"
)

// TestParseCockpitInfoParity pins BOTH shapes the Python _op_cockpit_info returns, so the
// sentinel and the real-info dict stay in sync with the selector (§2.5).
func TestParseCockpitInfoParity(t *testing.T) {
	// present: MCPCore loaded → cockpit_info() returns the flat coords dict.
	present := ParseCockpitInfo(json.RawMessage(`{"cockpit_port":51234,"session_epoch":"ep-1","token":"tok","protocol_version":1}`))
	if !present.Present {
		t.Fatal("flat coords dict should be Present")
	}
	if present.Port != 51234 || present.SessionEpoch != "ep-1" || present.Token != "tok" || present.ProtocolVersion != 1 {
		t.Fatalf("present parse = %+v", present)
	}
	if present.EditorAddr() != "127.0.0.1:51234" {
		t.Fatalf("EditorAddr = %q", present.EditorAddr())
	}

	// not present: MCPCore absent → the explicit sentinel.
	np := ParseCockpitInfo(json.RawMessage(`{"cockpit":"not_present"}`))
	if np.Present {
		t.Fatal("not_present sentinel must be Present=false")
	}

	// fail-safe: garbage / missing port → not present (stay on uexec).
	for _, bad := range []string{`{}`, `{"cockpit_port":0}`, `null`, `{"session_epoch":"x"}`} {
		if ParseCockpitInfo(json.RawMessage(bad)).Present {
			t.Fatalf("%q should parse as not-present", bad)
		}
	}
}
