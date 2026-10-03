package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/crash"
	"github.com/jdziat/unreal-mcp-server/internal/logs"
)

type editorEventsIn struct {
	SinceOffset int64  `json:"since_offset,omitempty" jsonschema:"byte marker from a previous call; 0 = from the start"`
	Type        string `json:"type,omitempty" jsonschema:"only return events of this type (e.g. issue)"`
	Limit       int    `json:"limit,omitempty" jsonschema:"max events returned (most recent kept); default 200"`
}

type sceneSnapshotIn struct {
	Name        string `json:"name,omitempty" jsonschema:"snapshot name; default 'auto'"`
	ClassFilter string `json:"class_filter,omitempty" jsonschema:"only snapshot actors matching this class/label substring"`
}

type sceneRestoreIn struct {
	Name string `json:"name,omitempty" jsonschema:"snapshot name to restore; default 'auto'"`
}

// registerRobustnessTools adds P5: non-blocking event observation (survives a
// busy command channel), a cheap liveness probe, snapshot/restore so an agent can
// undo a destructive experiment, and a post-rebuild health gate.
func registerRobustnessTools(s *mcp.Server, d Deps) {
	b := d.Bridge

	add(s, "editor_events",
		"Tail the editor's structured event stream (issues, PIE transitions) by byte offset — observable even while a long op holds the command channel. Returns events + a new offset to poll from.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in editorEventsIn) (*mcp.CallToolResult, map[string]any, error) {
			if resolveDeps(ctx, d).ProjectDir == "" {
				return nil, nil, errNoProject
			}
			evs, off, err := logs.Tail(logs.Path(resolveDeps(ctx, d).ProjectDir), in.SinceOffset)
			if err != nil {
				return nil, nil, err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 200
			}
			out := make([]json.RawMessage, 0, len(evs))
			for _, e := range evs {
				if in.Type != "" && e.Type != in.Type {
					continue
				}
				out = append(out, e.Raw)
			}
			if len(out) > limit {
				out = out[len(out)-limit:]
			}
			return nil, map[string]any{"events": out, "offset": off, "count": len(out)}, nil
		})

	add(s, "editor_ping",
		"Cheap liveness probe: is the editor reachable, what bridge version, is it in PIE. A fast heartbeat that doesn't touch assets.",
		structHandler[noArgs](b, "editor_ping", func(noArgs) map[string]any { return map[string]any{} }))

	add(s, "scene_snapshot",
		"Save the current level's actor transforms (by label) to a named snapshot file so a MOVE/rotate/scale experiment can be undone with scene_restore. Scope is transforms only — it does not record spawns/deletes.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in sceneSnapshotIn) (*mcp.CallToolResult, map[string]any, error) {
			if resolveDeps(ctx, d).ProjectDir == "" {
				return nil, nil, errNoProject
			}
			raw, err := bridgeFromCtx(ctx, b).Call(ctx, "actor_transforms", map[string]any{"class_filter": in.ClassFilter})
			if err != nil {
				return nil, nil, err
			}
			var r struct {
				Count      int              `json:"count"`
				Transforms []map[string]any `json:"transforms"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, nil, err
			}
			path := snapshotPath(resolveDeps(ctx, d).ProjectDir, in.Name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return nil, nil, err
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"snapshot": in.Name, "file": path, "actors": r.Count}, nil
		})

	add(s, "scene_restore",
		"Reposition existing actors to their snapshot transforms (undo a move/rotate/scale experiment). Transform-only: it does NOT recreate actors deleted since the snapshot or remove ones spawned since (delete those with delete_actor / pie_destroy). Reports labels no longer present.",
		func(ctx context.Context, _ *mcp.CallToolRequest, in sceneRestoreIn) (*mcp.CallToolResult, map[string]any, error) {
			if resolveDeps(ctx, d).ProjectDir == "" {
				return nil, nil, errNoProject
			}
			data, err := os.ReadFile(snapshotPath(resolveDeps(ctx, d).ProjectDir, in.Name))
			if err != nil {
				return nil, nil, err
			}
			var snap struct {
				Transforms json.RawMessage `json:"transforms"`
			}
			if err := json.Unmarshal(data, &snap); err != nil {
				return nil, nil, err
			}
			var transforms []any
			_ = json.Unmarshal(snap.Transforms, &transforms)
			raw, err := bridgeFromCtx(ctx, b).Call(ctx, "scene_restore", map[string]any{"transforms": transforms})
			if err != nil {
				return nil, nil, err
			}
			out := map[string]any{}
			_ = json.Unmarshal(raw, &out)
			return nil, out, nil
		})

	add(s, "health_check",
		"Post-rebuild/relaunch health gate: editor reachable (with the EXPECTED bridge version so a stale DLL is caught), and no crash since a given time. Use before trusting an editor after a compile.",
		healthCheck(b, d))
}

type healthCheckIn struct {
	ExpectVersion int    `json:"expect_version,omitempty" jsonschema:"fail if the live bridge version is below this (catches a stale/rolled-back module)"`
	SinceRFC3339  string `json:"since,omitempty" jsonschema:"treat crashes at/after this time as failures; default: last 10 min"`
}

func healthCheck(b *bridge.Bridge, d Deps) mcp.ToolHandlerFor[healthCheckIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in healthCheckIn) (*mcp.CallToolResult, map[string]any, error) {
		res := map[string]any{"healthy": true}
		var problems []string

		pingCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		raw, err := bridgeFromCtx(ctx, b).Call(pingCtx, "editor_ping", map[string]any{})
		if err != nil {
			return nil, map[string]any{"healthy": false, "problems": []string{"editor unreachable: " + err.Error()}}, nil
		}
		var ping struct {
			Version int  `json:"version"`
			PIE     bool `json:"pie"`
		}
		_ = json.Unmarshal(raw, &ping)
		res["version"] = ping.Version
		res["pie"] = ping.PIE
		if in.ExpectVersion > 0 && ping.Version < in.ExpectVersion {
			problems = append(problems, "bridge version "+strconv.Itoa(ping.Version)+" < expected "+strconv.Itoa(in.ExpectVersion)+" (stale module — rebuild/redeploy the bridge)")
		}

		since := time.Now().Add(-10 * time.Minute)
		if in.SinceRFC3339 != "" {
			if t, perr := time.Parse(time.RFC3339, in.SinceRFC3339); perr == nil {
				since = t
			}
		}
		if resolveDeps(ctx, d).ProjectDir != "" {
			if rep, _ := crash.FromCrashDir(resolveDeps(ctx, d).ProjectDir, since); rep != nil {
				problems = append(problems, "crash detected: "+rep.Summary)
				res["crash"] = rep
			}
		}
		if len(problems) > 0 {
			res["healthy"] = false
			res["problems"] = problems
		}
		return nil, res, nil
	}
}

func snapshotPath(projectDir, name string) string {
	if name == "" {
		name = "auto"
	}
	return filepath.Join(projectDir, ".mcp", "snapshots", name+".json")
}
