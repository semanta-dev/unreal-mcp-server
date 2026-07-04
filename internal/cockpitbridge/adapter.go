// Package cockpitbridge adapts the framed cockpit transport (internal/cockpit) to the
// semantic Bridge's native backend interface (internal/bridge.NativeDispatcher). It lives
// in its own package so neither the transport nor the semantic layer imports the other —
// the composition root wires them here (EDITOR_PLUGIN_PLAN.md §5.4 backend selector).
package cockpitbridge

import (
	"context"
	"encoding/json"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/cockpit"
)

// Adapter dispatches op results over a cockpit.Client's framed socket, presenting them as
// the flat bridge.NativeResult the semantic Bridge expects.
type Adapter struct {
	Client *cockpit.Client
}

// New wraps a cockpit.Client as a bridge.NativeDispatcher.
func New(c *cockpit.Client) *Adapter { return &Adapter{Client: c} }

// RPCNative sends the op over the framed channel and flattens the rpc_result frame into a
// bridge.NativeResult. A transport error (channel dropped) is returned as an error so the
// Bridge can fall back / surface it; an op-level failure is carried in the result's OK/Code.
func (a *Adapter) RPCNative(ctx context.Context, op string, args json.RawMessage, intent, taskID string) (bridge.NativeResult, error) {
	f, err := a.Client.RPC(ctx, op, args, intent, taskID)
	if err != nil {
		return bridge.NativeResult{}, err
	}
	ok := f.OK != nil && *f.OK
	return bridge.NativeResult{
		OK:        ok,
		Result:    f.Result,
		Error:     f.Error,
		Code:      f.Code,
		Retryable: f.Retryable,
		Traceback: f.Traceback,
	}, nil
}

// compile-time check that Adapter satisfies the Bridge's native backend interface.
var _ bridge.NativeDispatcher = (*Adapter)(nil)
