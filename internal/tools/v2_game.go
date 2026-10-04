package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jdziat/unreal-mcp-server/internal/session"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// The game's own agent interface (REMEDIATION_PLAN.md R1.4–R1.5): a project declares
// it in .umcp.json game_api; `game` reads it (pure getters only) and `game_command`
// runs its commands (always exec, idempotent through request_id + world_epoch).

func gameSpecs() []*spec.Spec { return []*spec.Spec{gameSpec(), gameCommandSpec()} }

// gameAPIOf loads the project's validated game_api, or explains why there is none.
func gameAPIOf(c *spec.Call) (*session.GameAPI, error) {
	if c.Deps.ProjectDir == "" {
		return nil, envelope.New(envelope.Precondition, "no project directory: the game API is declared in the project's .umcp.json")
	}
	pf, err := session.LoadProjectFile(c.Deps.ProjectDir)
	if err != nil {
		return nil, envelope.New(envelope.Precondition, "the project's .umcp.json is invalid: %v", err)
	}
	if pf.GameAPIErr != "" {
		return nil, envelope.New(envelope.Precondition, "%s", pf.GameAPIErr).WithHint("fix game_api in the project's .umcp.json")
	}
	if pf.GameAPI == nil {
		return nil, envelope.New(envelope.Precondition, "this project declares no game API").
			WithHint(`add "game_api" to .umcp.json: {"version": 1, "object": "@subsystem:<Module>.<Class>", "capabilities": ..., "snapshot": ..., "command": ..., "events": ...}`)
	}
	return pf.GameAPI, nil
}

// worldEpochs remembers each project's last-seen game world_epoch (from any game read),
// so game_command can name the world it means (GAME_CONTRACT.md).
var worldEpochs sync.Map // project dir -> epoch

// refusedIDs remembers request_ids the game refused as dedup_expired (per project): a
// refused id must not be re-sent — once the server learns the new world, the same id
// would run there, though the agent cannot know whether it already ran in the old one.
var refusedIDs sync.Map // project dir + "|" + request_id -> struct{}

func noteEpoch(c *spec.Call, out map[string]any) {
	if e, ok := out["world_epoch"].(string); ok && e != "" {
		worldEpochs.Store(c.Deps.ProjectDir, e)
	}
}

// gameRead calls one of the game API's read functions (capabilities, snapshot,
// events): the companion verifies it is BlueprintPure or const, calls it and decodes
// its JSON.
func gameRead(ctx context.Context, c *spec.Call, api *session.GameAPI, fn string, args []any) (map[string]any, error) {
	out, err := v2Op(ctx, c, "game_read", map[string]any{"class": api.ClassPath(), "function": fn, "args": args})
	if err != nil {
		return nil, err
	}
	res, _ := out["result"].(map[string]any)
	if res == nil {
		return nil, envelope.New(envelope.OperationFailed, "%s.%s did not return a JSON object", api.Class, fn)
	}
	noteEpoch(c, res)
	return res, nil
}

type gameIn struct {
	Op    string `json:"op" jsonschema:"capabilities | snapshot | events"`
	Since string `json:"since,omitempty" jsonschema:"events: the cursor from the last events/snapshot (default: from the start); another world's cursor reports gap"`
}

func gameSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "capabilities", Summary: "the game's commands (with tiers), event kinds, world_epoch", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"game_read"}, Needs: []string{"pie", "plugin>=3"}},
		{Name: "snapshot", Summary: "the game state, read-only", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"game_read"}, Needs: []string{"pie", "plugin>=3"}},
		{Name: "events", Summary: "gameplay events after a cursor (gap/dropped when some were lost)", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"game_read"}, Needs: []string{"pie", "plugin>=3"}},
	}
	return &spec.Spec{
		Name: "game", Title: "The game's own API (read)", Toolset: spec.Game, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Read the running game through its own agent API (the project's .umcp.json game_api; PIE). " +
			"capabilities: commands + world_epoch; snapshot: the game state; events since=<cursor>: {events, next_cursor, " +
			"gap, dropped}. Change the game with game_command.",
		Schema: spec.SchemaFor[gameIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			var in gameIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			api, err := gameAPIOf(c)
			if err != nil {
				return nil, err
			}
			fn, args := api.Capabilities, []any{}
			switch c.Op.Name {
			case "snapshot":
				fn = api.Snapshot
			case "events":
				fn, args = api.Events, []any{in.Since}
			}
			out, err := gameRead(ctx, c, api, fn, args)
			if err != nil {
				return nil, err
			}
			summary := c.Op.Name
			if evs, ok := out["events"].([]any); ok {
				summary = fmt.Sprintf("%d events (gap %v)", len(evs), out["gap"])
			}
			return &spec.Result{Data: out, Summary: summary}, nil
		},
	}
}

type gameCommandIn struct {
	Name      string         `json:"name" jsonschema:"the command (game op=capabilities lists them)"`
	Args      map[string]any `json:"args,omitempty" jsonschema:"the command's arguments"`
	RequestID string         `json:"request_id" jsonschema:"required, unique per intended action: re-sending the same id returns the recorded result and never runs it twice"`
}

func gameCommandSpec() *spec.Spec {
	return &spec.Spec{
		Name: "game_command", Title: "Run a game command", Toolset: spec.Game, Timeout: sync15, Max: sync28,
		// Exec, always: a game declares its own tiers (shown by game op=capabilities), but
		// gating, annotations and retries are decided from this static tier before the
		// call runs. Idempotent ONLY because the game deduplicates request_id within one
		// world (and refuses another world's epoch): a re-send never runs twice.
		Ops: []spec.OpSpec{{Tier: spec.Exec, Idempotent: true, Required: []string{"name", "request_id"}, Reaches: []string{"game_read", "game_command"}, Needs: []string{"pie", "plugin>=3"}}},
		Description: "Run one of the game's commands (its own API; PIE) → {accepted, result}. request_id is required: " +
			"after outcome:unknown, re-send the SAME request_id (the game returns the recorded result). A command for a " +
			"world that restarted is refused (dedup_expired): read the game again, then send it with a new request_id.",
		Schema: spec.SchemaFor[gameCommandIn](nil, "name", "request_id"),
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			var in gameCommandIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			api, err := gameAPIOf(c)
			if err != nil {
				return nil, err
			}
			if _, refused := refusedIDs.Load(c.Deps.ProjectDir + "|" + in.RequestID); refused {
				return nil, envelope.New(envelope.InvalidArgument, "request_id %q was refused for an earlier game world (dedup_expired)", in.RequestID).
					WithHint("send the command with a NEW request_id once you have read the new world")
			}
			epoch, _ := worldEpochs.Load(c.Deps.ProjectDir)
			if epoch == nil {
				// Never send a command without an epoch: learn the world first.
				if _, err := gameRead(ctx, c, api, api.Capabilities, []any{}); err != nil {
					return nil, err
				}
				epoch, _ = worldEpochs.Load(c.Deps.ProjectDir)
			}
			req := map[string]any{}
			for k, v := range in.Args {
				req[k] = v
			}
			req["command"], req["request_id"], req["world_epoch"] = in.Name, in.RequestID, epoch
			body, _ := json.Marshal(req)
			out, err := v2Op(ctx, c, "game_command", map[string]any{"class": api.ClassPath(), "function": api.Command, "request": string(body)})
			if err != nil {
				return nil, err
			}
			res, _ := out["result"].(map[string]any)
			if res == nil {
				return nil, envelope.New(envelope.OperationFailed, "%s.%s did not return a JSON object", api.Class, api.Command)
			}
			noteEpoch(c, res)
			if accepted, _ := res["accepted"].(bool); !accepted {
				code, msg := gameError(res)
				e := envelope.New(envelope.Precondition, "the game refused %s: %s", in.Name, msg).
					WithDetail("game_error", code).WithDetail("result", res)
				if code == "dedup_expired" {
					worldEpochs.Delete(c.Deps.ProjectDir)
					refusedIDs.Store(c.Deps.ProjectDir+"|"+in.RequestID, struct{}{})
					e.WithHint("the game world changed (PIE restarted?): read it again (game op=snapshot), then send the command with a NEW request_id")
				}
				return nil, e
			}
			return &spec.Result{Data: res, Summary: in.Name + " accepted"}, nil
		},
	}
}

// gameError reads a refusal: Aesir puts error_code/message at the top, poly-world an
// error object.
func gameError(res map[string]any) (string, string) {
	if e, ok := res["error"].(map[string]any); ok {
		return fmt.Sprint(e["code"]), fmt.Sprint(e["message"])
	}
	return fmt.Sprint(res["error_code"]), fmt.Sprint(res["message"])
}
