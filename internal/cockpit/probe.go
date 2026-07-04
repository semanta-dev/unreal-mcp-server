package cockpit

import (
	"encoding/json"
	"net"
	"strconv"
)

// CockpitInfo is the decoded result of the `cockpit_info` op — the native-presence probe
// (EDITOR_PLUGIN_PLAN.md §2.5). It is how the backend selector decides native vs uexec:
// Present==false (the {"cockpit":"not_present"} sentinel, or MCPCore absent) keeps the
// session on the uexec/Python fallback; Present==true carries the coords to dial.
type CockpitInfo struct {
	Present         bool   `json:"-"`
	Port            int    `json:"cockpit_port"`
	SessionEpoch    string `json:"session_epoch"`
	Token           string `json:"token"`
	ProtocolVersion int    `json:"protocol_version"`
}

// ParseCockpitInfo decodes the cockpit_info op result. It keys strictly off the
// discriminator: {"cockpit":"not_present"} → not present; a flat dict with a positive
// cockpit_port → present. Anything else is treated as not-present (fail safe to uexec).
func ParseCockpitInfo(result json.RawMessage) CockpitInfo {
	var sentinel struct {
		Cockpit string `json:"cockpit"`
	}
	if json.Unmarshal(result, &sentinel) == nil && sentinel.Cockpit == "not_present" {
		return CockpitInfo{Present: false}
	}
	var info CockpitInfo
	if err := json.Unmarshal(result, &info); err == nil && info.Port > 0 {
		info.Present = true
		return info
	}
	return CockpitInfo{Present: false}
}

// EditorAddr is the loopback address to dial for the framed cockpit channel.
func (i CockpitInfo) EditorAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(i.Port))
}
