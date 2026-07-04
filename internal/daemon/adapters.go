package daemon

import (
	"github.com/jdziat/unreal-mcp-server/internal/editorpool"
	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
)

// OSLiveness is the production editorpool.Liveness: real OS process liveness +
// creation-time identity (the recycled-PID guard). The standing heartbeat and the
// reaper use this to PID-gate the pool. Its methods are fast + side-effect-free +
// never call back into Pool, per the Liveness contract.
type OSLiveness struct{}

func (OSLiveness) IsAlive(pid int) bool    { return lifecycle.IsAlive(pid) }
func (OSLiveness) Identity(pid int) string { return lifecycle.ProcessIdentity(pid) }

// assert it satisfies the interface.
var _ editorpool.Liveness = OSLiveness{}
