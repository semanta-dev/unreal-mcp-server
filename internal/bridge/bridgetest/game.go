package bridgetest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// FakeGame models a game's agent API (docs/plans/GAME_CONTRACT.md) behind the
// companion's game_read / game_command ops: a world epoch per PIE session, a snapshot,
// an event journal with epoch cursors, and commands deduplicated by request_id.
type FakeGame struct {
	Class   string // the game_api subsystem class path
	Epoch   string
	Wave    int
	Journal []map[string]any // {seq, kind, ...}
	done    map[string]map[string]any
	nextSeq int
	Runs    int  // commands actually run (not deduplicated)
	NoEpoch bool // a non-conforming game: capabilities reports no world_epoch
}

// NewFakeGame is a game whose API subsystem is /Script/Game.GameAgentSubsystem.
func NewFakeGame() *FakeGame {
	return &FakeGame{Class: "/Script/Game.GameAgentSubsystem", Epoch: "epoch-1", done: map[string]map[string]any{}, nextSeq: 1}
}

// Restart models a new PIE session: a new epoch, an empty journal and dedup memory.
func (g *FakeGame) Restart(epoch string) {
	g.Epoch, g.Journal, g.done, g.nextSeq, g.Wave = epoch, nil, map[string]map[string]any{}, 1, 0
}

func (g *FakeGame) record(kind string, data map[string]any) {
	e := map[string]any{"seq": g.nextSeq, "kind": kind}
	for k, v := range data {
		e[k] = v
	}
	g.nextSeq++
	g.Journal = append(g.Journal, e)
}

func (w *World) gameObject(args map[string]any) *OpError {
	if w.pie == nil {
		return &OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
	}
	if args["class"] != w.Game.Class {
		return &OpError{Code: "NOT_FOUND", Message: fmt.Sprintf("no %v in the pie world", args["class"])}
	}
	return nil
}

func (w *World) gameRead(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.gameObject(args); err != nil {
		return nil, err
	}
	g := w.Game
	switch args["function"] {
	case "GetCapabilitiesJson":
		res := map[string]any{"world_epoch": g.Epoch, "commands": []any{map[string]any{"name": "start_wave", "tier": "ephemeral"}}}
		if g.NoEpoch {
			delete(res, "world_epoch")
		}
		return map[string]any{"result": res}, nil
	case "PeekSnapshotJson":
		return map[string]any{"result": map[string]any{"world_epoch": g.Epoch, "wave_number": g.Wave,
			"events_cursor": fmt.Sprintf("%s:%d", g.Epoch, g.nextSeq-1)}}, nil
	case "GetEventsSince":
		cursor := ""
		if a, _ := args["args"].([]any); len(a) > 0 {
			cursor, _ = a[0].(string)
		}
		after, gap, reason := 0, false, ""
		if cursor != "" {
			epoch, seq, _ := strings.Cut(cursor, ":")
			if epoch != g.Epoch {
				gap, reason = true, "world_changed"
			} else {
				after, _ = strconv.Atoi(seq)
			}
		}
		var evs []any
		last := after
		for _, e := range g.Journal {
			if s := e["seq"].(int); s > after {
				evs = append(evs, e)
				last = s
			}
		}
		res := map[string]any{"world_epoch": g.Epoch, "events": evs, "gap": gap, "dropped": 0,
			"next_cursor": fmt.Sprintf("%s:%d", g.Epoch, last)}
		if reason != "" {
			res["reason"] = reason
		}
		return map[string]any{"result": res}, nil
	}
	return nil, &OpError{Code: "BAD_VALUE", Message: fmt.Sprintf("%v is not BlueprintPure or const", args["function"])}
}

func (w *World) gameCommand(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.gameObject(args); err != nil {
		return nil, err
	}
	g := w.Game
	var req map[string]any
	if s, _ := args["request"].(string); json.Unmarshal([]byte(s), &req) != nil {
		return nil, &OpError{Code: "BAD_VALUE", Message: "the request is not JSON"}
	}
	id, _ := req["request_id"].(string)
	resp := func(ok bool, code, msg string, result map[string]any) map[string]any {
		r := map[string]any{"accepted": ok, "request_id": id, "world_epoch": g.Epoch}
		if code != "" {
			r["error_code"], r["message"] = code, msg
		}
		if result != nil {
			r["result"] = result
		}
		return r
	}
	switch {
	case id == "":
		return map[string]any{"result": resp(false, "invalid_argument", "request_id is required", nil)}, nil
	case req["world_epoch"] != g.Epoch:
		return map[string]any{"result": resp(false, "dedup_expired", "world_epoch does not match this world", nil)}, nil
	}
	if prior, ok := g.done[id]; ok {
		return map[string]any{"result": prior}, nil
	}
	var r map[string]any
	switch req["command"] {
	case "start_wave":
		g.Runs++
		g.Wave++
		g.record("wave_start", map[string]any{"data": map[string]any{"wave": g.Wave}})
		r = resp(true, "", "", map[string]any{"wave_number": g.Wave})
	default:
		r = resp(false, "unknown_command", fmt.Sprintf("no command %v", req["command"]), nil)
	}
	g.done[id] = r
	return map[string]any{"result": r}, nil
}

// observePaths models the companion's observe_paths over SetObject objects: a path is
// "@ref.Getter().field…" where the getter is a prop holding a JSON string (or a value).
func (w *World) observePaths(args map[string]any) (any, *OpError) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pie == nil {
		return nil, &OpError{Code: "NOT_IN_PIE", Message: "PIE is not running"}
	}
	values, errs := map[string]any{}, map[string]any{}
	paths, _ := args["paths"].([]any)
	for _, p := range paths {
		path, _ := p.(string)
		head, rest, _ := strings.Cut(path, ".")
		obj, ok := w.objects[head]
		if !ok {
			errs[path] = "no object " + head
			continue
		}
		var cur any = obj.Properties
		for _, seg := range strings.Split(rest, ".") {
			name := strings.TrimSuffix(seg, "()")
			m, _ := cur.(map[string]any)
			cur = m[name]
			if s, isStr := cur.(string); isStr && strings.HasSuffix(seg, "()") {
				var decoded any
				if json.Unmarshal([]byte(s), &decoded) == nil {
					cur = decoded
				}
			}
		}
		values[path] = cur
	}
	return map[string]any{"world": "pie", "values": values, "errors": errs}, nil
}
