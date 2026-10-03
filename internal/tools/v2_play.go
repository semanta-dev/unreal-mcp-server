package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/crash"
	"github.com/jdziat/unreal-mcp-server/internal/eval"
	"github.com/jdziat/unreal-mcp-server/internal/scenespec"
	"github.com/jdziat/unreal-mcp-server/internal/snapshot"
	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"github.com/jdziat/unreal-mcp-server/internal/visual"
)

// v2 play / world / snapshot / visual / scene / audio tools (OVERHAUL_PLAN.md §2.3
// rows 16–25, 27; §2.5).

func playSpecs() []*spec.Spec {
	return []*spec.Spec{pieSpec(), pieObserveSpec(), pieWaitSpec(), worldQuerySpec(), snapshotSpec(),
		snapshotRestoreSpec(), screenshotSpec(), captureSpec(), sceneSpec(), sceneClearSpec(), audioSpec()}
}

// pollTimeout is how long a polling loop may run: the requested window (default
// def), cut short so the final answer (met:false) is returned before the call's own
// deadline — keeping a margin of a fifth of the window, at most a second.
func pollTimeout(ctx context.Context, requestedS float64, def time.Duration) time.Duration {
	timeout := def
	if requestedS > 0 {
		timeout = time.Duration(requestedS * float64(time.Second))
	}
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if lim := left - min(time.Second, left/5); lim < timeout {
			timeout = lim
		}
	}
	return timeout
}

// --- pie -------------------------------------------------------------------------

type pieIn struct {
	Op        string  `json:"op" jsonschema:"start | stop | input"`
	Simulate  bool    `json:"simulate,omitempty" jsonschema:"start: Simulate In Editor (the world runs, no player is possessed)"`
	Wait      *bool   `json:"wait,omitempty" jsonschema:"start/stop: wait until PIE is actually running/stopped (default true)"`
	Key       string  `json:"key,omitempty" jsonschema:"input: UE key name — W, A, S, D, SpaceBar, LeftMouseButton, Gamepad_FaceButton_Bottom, ..."`
	Action    string  `json:"action,omitempty" jsonschema:"input: tap (default) | press | release | hold | release_all"`
	DurationS float64 `json:"duration_s,omitempty" jsonschema:"input action=hold: seconds (default 1)"`
}

func pieSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "start", Summary: "start Play In Editor (or Simulate)", Tier: spec.Ephemeral, Idempotent: true, Rejects: []string{"key", "action", "duration_s"}, Reaches: []string{"pie_start", "editor_ping"}},
		{Name: "stop", Summary: "stop PIE (game-world changes are discarded)", Tier: spec.Ephemeral, Idempotent: true, Rejects: []string{"simulate", "key", "action", "duration_s"}, Reaches: []string{"pie_stop", "editor_ping"}},
		{Name: "input", Summary: "inject a key/button into the running game", Tier: spec.Ephemeral, Required: []string{"key"}, Rejects: []string{"simulate", "wait"}, Reaches: []string{"pie_input"}},
	}
	return &spec.Spec{
		Name: "pie", Title: "Play In Editor", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Run the game inside the editor and drive it.\n" +
			"- op=start: begin PIE (simulate=true: no possessed player); waits until it is running.\n" +
			"- op=stop: end PIE; waits until it has stopped. Everything changed in the pie world is discarded.\n" +
			"- op=input: tap/press/release/hold `key` as if a player did (needs the UnrealMCP plugin), e.g. hold W to walk.",
		Schema: spec.SchemaFor[pieIn](map[string][]any{"op": spec.OpEnum(ops...),
			"action": {"tap", "press", "release", "hold", "release_all"}}, "op"),
		Replaces: []string{"start_play", "stop_play", "pie_input"},
		Handler:  pieHandler,
	}
}

func pieHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in pieIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if c.Op.Name == "input" {
		out, err := v2Op(ctx, c, "pie_input", pick(c.Args, "key", "action", "duration_s"))
		return &spec.Result{Data: out, Summary: fmt.Sprintf("%s %s", orStr(in.Action, "tap"), in.Key)}, err
	}
	want := c.Op.Name == "start"
	py := map[bool]string{true: "pie_start", false: "pie_stop"}[want]
	out, err := v2Op(ctx, c, py, pick(c.Args, "simulate"))
	if err != nil || (in.Wait != nil && !*in.Wait) {
		return &spec.Result{Data: out, Summary: "PIE " + c.Op.Name + " requested"}, err
	}
	// PIE begins/ends on a later editor tick: poll until the state flips.
	started := time.Now()
	deadline := started.Add(pollTimeout(ctx, 0, 15*time.Second))
	for {
		ping, perr := v2Op(ctx, c, "editor_ping", nil)
		if perr == nil && ping["pie"] == want {
			out["pie"] = want
			return &spec.Result{Data: out, Summary: map[bool]string{true: "PIE is running", false: "PIE stopped"}[want]}, nil
		}
		if time.Now().After(deadline) {
			if perr != nil {
				// The editor stopped answering (e.g. crashed on PIE start): say so, with the crash if any.
				e := envelope.Classify(perr, true)
				if c.Deps.ProjectDir != "" {
					if rep, _ := crash.FromCrashDir(c.Deps.ProjectDir, started); rep != nil {
						e.WithDetail("crash", rep)
					}
				}
				return nil, e
			}
			return nil, envelope.New(envelope.Timeout, "PIE did not %s in time", c.Op.Name).
				WithHint("check the editor log (logs op=tail) — a compile error or a modal dialog can block PIE")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// --- pie_observe / pie_wait ------------------------------------------------------

type pieObserveIn struct {
	Actors     []string `json:"actors,omitempty" jsonschema:"actor labels to read detailed state for (unknown labels are listed in missing)"`
	Pawn       bool     `json:"pawn,omitempty" jsonschema:"include the player pawn's location, velocity and speed"`
	Player     int      `json:"player,omitempty" jsonschema:"pawn: local player index (default 0)"`
	Include    []string `json:"include,omitempty" jsonschema:"glob patterns of property names to include (default all, minus engine noise)"`
	Exclude    []string `json:"exclude,omitempty" jsonschema:"glob patterns of property names to exclude"`
	Properties []string `json:"properties,omitempty" jsonschema:"read exactly these properties (keeps your key names for predicates)"`
	MaxProps   int      `json:"max_props,omitempty" jsonschema:"cap on properties per object (default 48)"`
}

func pieObserveSpec() *spec.Spec {
	return &spec.Spec{
		Name: "pie_observe", Title: "Observe the running game", Toolset: spec.Core, Timeout: sync15, Max: sync28,
		Ops: []spec.OpSpec{{Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"pie_observe"}}},
		Description: "Read the running game (PIE): gamestate properties (discovered by reflection), a class histogram " +
			"`counts`, detailed state for `actors`, and with pawn=true the player pawn's location/velocity/speed. " +
			"The output schema is what pie_wait predicates address (gamestate.<prop>, counts.<Class>, pawn.speed).",
		Schema:   spec.SchemaFor[pieObserveIn](nil),
		Replaces: []string{"pie_observe", "pawn_state"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			out, err := v2Op(ctx, c, "pie_observe", pick(c.Args, "actors", "pawn", "player", "include", "exclude", "properties", "max_props"))
			if err != nil {
				return nil, err
			}
			n := 0
			if counts, ok := out["counts"].(map[string]any); ok {
				for _, v := range counts {
					f, _ := v.(float64)
					n += int(f)
				}
			}
			return &spec.Result{Data: out, Summary: fmt.Sprintf("%d actors in the running game", n)}, nil
		},
	}
}

type pieWaitIn struct {
	Predicate  string   `json:"predicate" jsonschema:"one comparison over pie_observe output, e.g. 'gamestate.wave_number >= 2', 'counts.EnemyCharacter >= 1', 'pawn.speed > 100'"`
	TimeoutS   float64  `json:"timeout_s,omitempty" jsonschema:"give up after this many seconds (default 20, max 28)"`
	IntervalS  float64  `json:"interval_s,omitempty" jsonschema:"seconds between observations (default 0.25)"`
	Properties []string `json:"properties,omitempty" jsonschema:"pin exact gamestate property names so the predicate can use them verbatim"`
	Pawn       bool     `json:"pawn,omitempty" jsonschema:"observe the pawn too (needed for pawn.* predicates)"`
}

func pieWaitSpec() *spec.Spec {
	return &spec.Spec{
		Name: "pie_wait", Title: "Wait for a game condition", Toolset: spec.Core, Timeout: sync28, Max: sync28,
		Ops: []spec.OpSpec{{Tier: spec.ReadOnly, Idempotent: true, Required: []string{"predicate"}, Reaches: []string{"pie_observe"}}},
		Description: "Poll pie_observe until `predicate` holds → {met, pie_running, elapsed_s, polls, final_state}. " +
			"met=false on timeout is a normal answer, not an error. Waits through PIE starting up; returns at once if PIE " +
			"stops. Read-only: to poll a UFUNCTION's result use actor_call with until.",
		Schema:   spec.SchemaFor[pieWaitIn](nil, "predicate"),
		Replaces: []string{"pie_wait_until"},
		Handler:  pieWait,
	}
}

func pieWait(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in pieWaitIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	pred, err := eval.ParsePredicate(in.Predicate)
	if err != nil {
		return nil, envelope.New(envelope.InvalidArgument, "predicate: %v", err)
	}
	timeout := pollTimeout(ctx, in.TimeoutS, 20*time.Second)
	interval := 250 * time.Millisecond
	if in.IntervalS > 0 {
		interval = time.Duration(in.IntervalS * float64(time.Second))
	}
	args := pick(c.Args, "properties", "pawn")
	start := time.Now()
	deadline := start.Add(timeout)
	polls := 0
	seen := false // PIE observed running at least once
	var last map[string]any
	answer := func(met, running bool, why string) *spec.Result {
		return &spec.Result{Data: map[string]any{"met": met, "pie_running": running, "elapsed_s": time.Since(start).Seconds(),
			"polls": polls, "final_state": last}, Summary: why}
	}
	for {
		out, err := v2Op(ctx, c, "pie_observe", args)
		polls++
		running := err == nil
		if err != nil {
			var oe *bridge.OpError
			// PIE still starting (NOT_IN_PIE) or a transient editor error: keep waiting —
			// unless PIE was running and has now ended (game over / stopped): the answer is final.
			if !errors.As(err, &oe) || !(oe.Retryable || oe.Code == "NOT_IN_PIE") {
				return nil, err
			}
			if oe.Code == "NOT_IN_PIE" && seen {
				return answer(false, false, "PIE stopped before the condition was met"), nil
			}
		} else {
			seen, last = true, out
			if ok, _ := pred.Eval(out); ok {
				return answer(true, true, "condition met after "+strconv.Itoa(polls)+" polls"), nil
			}
		}
		if time.Now().Add(interval).After(deadline) {
			why := "condition not met within " + timeout.Round(time.Millisecond).String()
			if !seen {
				why += " (PIE never ran — start it with pie op=start)"
			}
			return answer(false, running, why), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// --- world_query -----------------------------------------------------------------

type worldQueryIn struct {
	Op     string    `json:"op" jsonschema:"line_trace | sphere_overlap | nav_path | project_point | instances_count | instances_list"`
	World  string    `json:"world,omitempty" jsonschema:"editor (default) | pie | auto"`
	Start  []float64 `json:"start,omitempty" jsonschema:"line_trace/nav_path: [x, y, z]"`
	End    []float64 `json:"end,omitempty" jsonschema:"line_trace/nav_path: [x, y, z]"`
	Center []float64 `json:"center,omitempty" jsonschema:"sphere_overlap: [x, y, z]"`
	Radius float64   `json:"radius,omitempty" jsonschema:"sphere_overlap: radius (default 100)"`
	Point  []float64 `json:"point,omitempty" jsonschema:"project_point: [x, y, z]"`
	Tag    string    `json:"tag,omitempty" jsonschema:"instances_*: only ISM/HISM components with this component tag"`
	Mesh   string    `json:"mesh,omitempty" jsonschema:"instances_*: only components whose mesh path contains this"`
	Limit  int       `json:"limit,omitempty" jsonschema:"instances_list: max instances (default 8192; truncated:true when cut)"`
}

func worldQuerySpec() *spec.Spec {
	q := func(name, summary, py string, req ...string) spec.OpSpec {
		return spec.OpSpec{Name: name, Summary: summary, Tier: spec.ReadOnly, Idempotent: true, Required: req, Reaches: []string{py}}
	}
	ops := []spec.OpSpec{
		q("line_trace", "is the line from start to end blocked, and by what", "world_query", "start", "end"),
		q("sphere_overlap", "actors overlapping a sphere", "world_query", "center"),
		q("nav_path", "can the AI walk from start to end", "world_query", "start", "end"),
		q("project_point", "is the point on the navmesh", "world_query", "point"),
		q("instances_count", "ISM/HISM instance counts by mesh", "instances_count"),
		q("instances_list", "ISM/HISM instance transforms", "instances_list"),
	}
	return &spec.Spec{
		Name: "world_query", Title: "Spatial queries", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Ask the world spatial questions (world=editor default, pie, or auto). Every result echoes the world.\n" +
			"- op=line_trace / sphere_overlap: collision (line of sight, what is near a point).\n" +
			"- op=nav_path / project_point: navigation (needs a built navmesh).\n" +
			"- op=instances_count / instances_list: ISM/HISM instances — a whole city can live as instances inside ONE actor, " +
			"invisible to actor_query.",
		Schema:   spec.SchemaFor[worldQueryIn](map[string][]any{"op": spec.OpEnum(ops...), "world": {"editor", "pie", "auto"}}, "op"),
		Replaces: []string{"world_query", "instances_count", "instances_list"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			world, _ := c.Args["world"].(string)
			world = orStr(world, "editor")
			py := c.Op.Name
			var args map[string]any
			if py == "instances_count" || py == "instances_list" {
				args = pick(c.Args, "tag", "mesh", "limit")
			} else {
				py = "world_query"
				args = pick(c.Args, "start", "end", "center", "radius", "point")
				args["kind"] = c.Op.Name
			}
			args["world"] = world
			out, err := v2Op(ctx, c, py, args)
			if err != nil {
				return nil, err
			}
			if _, ok := out["world"]; !ok {
				out["world"] = world
			}
			return &spec.Result{Data: out, Summary: c.Op.Name + " done"}, nil
		},
	}
}

// --- snapshot / snapshot_restore -------------------------------------------------

type snapshotIn struct {
	Op          string  `json:"op" jsonschema:"take | diff | list | digest"`
	Name        string  `json:"name,omitempty" jsonschema:"take: snapshot name (default auto; overwrites); diff: the BEFORE snapshot"`
	Against     string  `json:"against,omitempty" jsonschema:"diff: the AFTER snapshot (default: the level right now)"`
	ClassFilter string  `json:"class_filter,omitempty" jsonschema:"take/digest scope=actors: only actors whose class or label contains this"`
	Scope       string  `json:"scope,omitempty" jsonschema:"digest: instances (ISM/HISM, default) | actors"`
	Tag         string  `json:"tag,omitempty" jsonschema:"digest scope=instances: component tag filter"`
	Mesh        string  `json:"mesh,omitempty" jsonschema:"digest scope=instances: mesh path substring filter"`
	PosBucket   float64 `json:"pos_bucket,omitempty" jsonschema:"digest: position quantization in world units (default 1)"`
	RotBucket   float64 `json:"rot_bucket,omitempty" jsonschema:"digest: rotation quantization in degrees (default 1)"`
	Limit       int     `json:"limit,omitempty" jsonschema:"digest scope=instances: max instances hashed (default 5,000,000; a larger set fails rather than hashing part of it)"`
}

func snapshotSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "take", Summary: "record every actor's path, class, tags and transform", Tier: spec.Ephemeral, Idempotent: true, Reaches: []string{"snapshot_actors"}},
		{Name: "diff", Summary: "added / removed / moved / retagged between two snapshots (or now)", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"name"}, Reaches: []string{"snapshot_actors"}},
		{Name: "list", Summary: "stored snapshots", Tier: spec.ReadOnly, Idempotent: true},
		{Name: "digest", Summary: "deterministic hash of actor or instance transforms", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"snapshot_actors", "instances_list"}},
	}
	return &spec.Spec{
		Name: "snapshot", Title: "Level snapshots", Toolset: spec.Core, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Record and compare the editor level (files under Saved/MCP/snapshots).\n" +
			"- op=take: store `name` (default auto) — actors matched by object path, with class, tags, transform.\n" +
			"- op=diff: `name` vs `against` (default: the level now) → added, removed, moved, retagged; under World " +
			"Partition, actors in a cell that was not loaded are `unknown`, never removed.\n" +
			"- op=list: stored snapshots.\n" +
			"- op=digest: a quantized SHA1 over actor (scope=actors) or ISM/HISM instance transforms — verify a level hashes " +
			"to an expected value. Does not store anything.\n" +
			"Undo moves with snapshot_restore.",
		Schema:   spec.SchemaFor[snapshotIn](map[string][]any{"op": spec.OpEnum(ops...), "scope": {"instances", "actors"}}, "op"),
		Replaces: []string{"level_snapshot", "level_diff", "scene_snapshot", "scene_digest"},
		Handler:  snapshotHandler,
	}
}

// currentSnapshot asks the editor for its actors as a snapshot.File.
func currentSnapshot(ctx context.Context, c *spec.Call, name, classFilter string) (*snapshot.File, error) {
	b, err := v2Bridge(c)
	if err != nil {
		return nil, err
	}
	raw, err := b.Call(ctx, "snapshot_actors", map[string]any{"class_filter": classFilter})
	if err != nil {
		return nil, err
	}
	f := &snapshot.File{Name: name, TakenAt: time.Now().UTC()}
	if err := json.Unmarshal(raw, f); err != nil {
		return nil, fmt.Errorf("decode snapshot_actors: %w", err)
	}
	return f, nil
}

func snapshotName(s string) (string, error) {
	if s == "" {
		s = "auto"
	}
	if !snapshot.ValidName(s) {
		return "", envelope.New(envelope.InvalidArgument, "snapshot name %q: use 1-64 letters, digits, '_' or '-'", s)
	}
	return s, nil
}

func loadSnapshot(dir, name string) (*snapshot.File, error) {
	f, err := snapshot.Load(dir, name)
	if errors.Is(err, snapshot.ErrNotFound) {
		return nil, envelope.New(envelope.NotFound, "no snapshot named %q", name).WithHint("snapshot op=list shows the stored ones")
	}
	return f, err
}

func snapshotHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in snapshotIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if c.Op.Name == "digest" {
		return snapshotDigest(ctx, c, in)
	}
	dir, err := projectDir(c)
	if err != nil {
		return nil, err
	}
	switch c.Op.Name {
	case "list":
		l, err := snapshot.List(dir)
		if err != nil {
			return nil, err
		}
		return &spec.Result{Data: map[string]any{"snapshots": l}, Summary: fmt.Sprintf("%d snapshots", len(l))}, nil
	case "take":
		name, err := snapshotName(in.Name)
		if err != nil {
			return nil, err
		}
		f, err := currentSnapshot(ctx, c, name, in.ClassFilter)
		if err != nil {
			return nil, err
		}
		p, err := snapshot.Save(dir, f)
		if err != nil {
			return nil, err
		}
		return &spec.Result{Data: map[string]any{"name": name, "file": p, "actors": len(f.Actors), "world_partition": f.WorldPartition},
			Summary: fmt.Sprintf("snapshot %s: %d actors", name, len(f.Actors))}, nil
	}
	// diff
	name, err := snapshotName(in.Name)
	if err != nil {
		return nil, err
	}
	a, err := loadSnapshot(dir, name)
	if err != nil {
		return nil, err
	}
	var b *snapshot.File
	if in.Against != "" {
		against, err := snapshotName(in.Against)
		if err != nil {
			return nil, err
		}
		if b, err = loadSnapshot(dir, against); err != nil {
			return nil, err
		}
		if b.ClassFilter != a.ClassFilter {
			return nil, envelope.New(envelope.InvalidArgument, "%s was taken with class_filter %q and %s with %q; they cannot be compared",
				name, a.ClassFilter, against, b.ClassFilter)
		}
	} else if b, err = currentSnapshot(ctx, c, "current", a.ClassFilter); err != nil { // compare like with like
		return nil, err
	}
	d := snapshot.Diff(a, b)
	return &spec.Result{Data: map[string]any{"before": name, "after": orStr(in.Against, "current"), "diff": d},
		Summary: fmt.Sprintf("+%d -%d moved %d retagged %d unknown %d", len(d.Added), len(d.Removed), len(d.Moved), len(d.Retagged), len(d.Unknown))}, nil
}

func snapshotDigest(ctx context.Context, c *spec.Call, in snapshotIn) (*spec.Result, error) {
	scope := orStr(in.Scope, "instances")
	var items []snapshot.Transform
	if scope == "actors" {
		f, err := currentSnapshot(ctx, c, "digest", in.ClassFilter)
		if err != nil {
			return nil, err
		}
		for _, a := range f.Actors {
			items = append(items, snapshot.Transform{Mesh: a.Class, Loc: a.Loc, Rot: a.Rot, Scale: a.Scale})
		}
	} else {
		limit := in.Limit
		if limit <= 0 {
			limit = 5000000
		}
		args := map[string]any{"world": "editor", "limit": limit}
		if in.Tag != "" {
			args["tag"] = in.Tag
		}
		if in.Mesh != "" {
			args["mesh"] = in.Mesh
		}
		b, err := v2Bridge(c)
		if err != nil {
			return nil, err
		}
		raw, err := b.Call(ctx, "instances_list", args)
		if err != nil {
			return nil, err
		}
		var r struct {
			Instances []snapshot.Transform `json:"instances"`
			Truncated bool                 `json:"truncated"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if r.Truncated {
			// A content oracle never hashes a partial set.
			return nil, envelope.New(envelope.OperationFailed, "more than %d instances; the digest would cover only part of them", limit).
				WithHint("narrow with tag/mesh or raise limit")
		}
		items = r.Instances
	}
	res := snapshot.Digest(items, snapshot.Quant{PosBucket: in.PosBucket, RotBucket: in.RotBucket})
	return &spec.Result{Data: map[string]any{"scope": scope, "hash": res.Hash, "count": res.Count,
		"worst_pos_margin_uu": res.WorstPosMargin, "worst_rot_margin_deg": res.WorstRotMargin},
		Summary: fmt.Sprintf("%s digest %s over %d items", scope, res.Hash, res.Count)}, nil
}

type snapshotRestoreIn struct {
	Name string `json:"name,omitempty" jsonschema:"the snapshot to restore (default auto)"`
	Save *bool  `json:"save,omitempty" jsonschema:"save the level afterwards (default true)"`
}

func snapshotRestoreSpec() *spec.Spec {
	return &spec.Spec{
		Name: "snapshot_restore", Title: "Restore a snapshot", Toolset: spec.Core, Timeout: sync25, Max: sync28,
		Ops: []spec.OpSpec{{Tier: spec.Destructive, Idempotent: true, Reaches: []string{"snapshot_restore"}}},
		Description: "Move every actor that still exists back to its transform in snapshot `name` (matched by object path; " +
			"parents before attached children), as one undo step, then save. TRANSFORMS ONLY: actors spawned since are not " +
			"deleted and deleted actors are not recreated — both are listed in not_restored {added, removed} (and, under " +
			"World Partition, `unknown` for actors in cells that are not loaded). For those use scene_clear or git_revert.",
		Schema:   spec.SchemaFor[snapshotRestoreIn](nil),
		Replaces: []string{"scene_restore"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			var in snapshotRestoreIn
			if err := c.Decode(&in); err != nil {
				return nil, err
			}
			dir, err := projectDir(c)
			if err != nil {
				return nil, err
			}
			name, err := snapshotName(in.Name)
			if err != nil {
				return nil, err
			}
			f, err := loadSnapshot(dir, name)
			if err != nil {
				return nil, err
			}
			args := map[string]any{"name": name, "actors": f.Actors, "unloaded": f.Unloaded}
			if in.Save != nil {
				args["save"] = *in.Save
			}
			out, err := v2Op(ctx, c, "snapshot_restore", args)
			if err != nil {
				return nil, err
			}
			return &spec.Result{Data: out, Summary: fmt.Sprintf("restored %v actors from %s", out["restored"], name)}, nil
		},
	}
}

// --- screenshot ------------------------------------------------------------------

type screenshotIn struct {
	Op         string    `json:"op" jsonschema:"viewport | pie | orbit"`
	Width      int       `json:"width,omitempty" jsonschema:"viewport/pie: pixels (default 1280 / 1920)"`
	Height     int       `json:"height,omitempty" jsonschema:"viewport/pie: pixels (default 720 / 1080)"`
	Location   []float64 `json:"location,omitempty" jsonschema:"viewport: camera [x, y, z] (default: the editor viewport camera)"`
	Rotation   []float64 `json:"rotation,omitempty" jsonschema:"viewport: camera [pitch, yaw, roll]"`
	Actors     []string  `json:"actors,omitempty" jsonschema:"orbit: actor labels or globs to frame (default: the whole level)"`
	NumAngles  int       `json:"num_angles,omitempty" jsonschema:"orbit: angles around the target (default 8)"`
	Elevation  *float64  `json:"elevation,omitempty" jsonschema:"orbit: degrees above the horizon (default 25)"`
	Fov        float64   `json:"fov,omitempty" jsonschema:"orbit: vertical field of view (default 60)"`
	Fill       float64   `json:"fill,omitempty" jsonschema:"orbit: fraction of the frame the target fills (default 0.7)"`
	Cols       int       `json:"cols,omitempty" jsonschema:"orbit: contact-sheet columns (default 4)"`
	CellWidth  int       `json:"cell_width,omitempty" jsonschema:"orbit: per-angle width (default 480)"`
	CellHeight int       `json:"cell_height,omitempty" jsonschema:"orbit: per-angle height (default 270)"`
}

func screenshotSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "viewport", Summary: "render the editor world from the viewport (or a given) camera", Tier: spec.Ephemeral, Idempotent: true, Reaches: []string{"take_screenshot"}},
		{Name: "pie", Summary: "the running game's screen (HighResShot)", Tier: spec.Ephemeral, Idempotent: true, Reaches: []string{"pie_screenshot"}},
		{Name: "orbit", Summary: "N angles around a target as one contact sheet", Tier: spec.Ephemeral, Idempotent: true, Reaches: []string{"scene_bounds", "capture_poses"}},
	}
	return &spec.Spec{
		Name: "screenshot", Title: "Screenshot", Toolset: spec.Core, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Look at the world; returns PNG image content.\n" +
			"- op=viewport: the editor world via a scene capture (works with the editor in the background).\n" +
			"- op=pie: the running game's rendered screen (needs a visible game viewport).\n" +
			"- op=orbit: `actors` (or the level) from num_angles angles in one contact sheet — a quick all-sides check.\n" +
			"Transient capture actors may dirty the level; the result lists any map they dirtied.",
		Schema:   spec.SchemaFor[screenshotIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces: []string{"take_screenshot", "pie_screenshot", "scene_contact_sheet"},
		Handler:  screenshot,
	}
}

func screenshot(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in screenshotIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if c.Op.Name == "orbit" {
		return orbitShot(ctx, c, in)
	}
	fname := fmt.Sprintf("mcp_%d_%d.png", os.Getpid(), time.Now().UnixNano())
	args := map[string]any{"filename": fname}
	if in.Width > 0 {
		args["width"] = in.Width
	}
	if in.Height > 0 {
		args["height"] = in.Height
	}
	py, wait := "take_screenshot", 10*time.Second
	if c.Op.Name == "pie" {
		py, wait = "pie_screenshot", 20*time.Second // HighResShot writes asynchronously
	} else {
		if len(in.Location) > 0 {
			args["camera_location"] = in.Location
		}
		if len(in.Rotation) > 0 {
			args["camera_rotation_pyr"] = in.Rotation
		}
	}
	out, err := v2Op(ctx, c, py, args)
	if err != nil {
		return nil, err
	}
	file, _ := out["file"].(string)
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
		wait = time.Until(dl) - 500*time.Millisecond
	}
	if c.Op.Name == "pie" {
		file = awaitShot(file, wait)
	}
	img, err := readImageFile(file, wait)
	if err != nil {
		return nil, withLog(envelope.New(envelope.OperationFailed, "%v", err), out)
	}
	delete(out, "file")
	delete(out, "async")
	return &spec.Result{Data: out, Content: []mcp.Content{&mcp.ImageContent{Data: img, MIMEType: "image/png"}},
		Summary: c.Op.Name + " screenshot"}, nil
}

func orbitShot(ctx context.Context, c *spec.Call, in screenshotIn) (*spec.Result, error) {
	bargs := map[string]any{}
	if len(in.Actors) > 0 {
		bargs["globs"] = in.Actors
	}
	bounds, err := v2Op(ctx, c, "scene_bounds", bargs)
	if err != nil {
		return nil, err
	}
	if n, _ := bounds["count"].(float64); n == 0 {
		return nil, envelope.New(envelope.NotFound, "nothing to frame: no actor matched %v", in.Actors)
	}
	comb, _ := bounds["combined"].(map[string]any)
	box := visual.Bounds{Origin: anyVec3(comb["origin"]), Extent: anyVec3(comb["extent"])}
	num := orDefaultInt(in.NumAngles, 8)
	elevation := 25.0
	if in.Elevation != nil {
		elevation = *in.Elevation
	}
	fov, fill := orDefault(in.Fov, 60), orDefault(in.Fill, 0.7)
	poses := make([]map[string]any, num)
	for i := range poses {
		p := visual.FrameShot(box, 360*float64(i)/float64(num), elevation, fov, fill)
		poses[i] = map[string]any{"location": p.Location[:], "rotation_pyr": p.RotationPyr[:]}
	}
	b, err := v2Bridge(c)
	if err != nil {
		return nil, err
	}
	raw, err := b.Call(ctx, "capture_poses", map[string]any{"poses": poses, "world": "editor",
		"cell_width": orDefaultInt(in.CellWidth, 480), "cell_height": orDefaultInt(in.CellHeight, 270)})
	if err != nil {
		return nil, err
	}
	var pr struct {
		Dir   string `json:"dir"`
		Cells []struct {
			Index       int       `json:"index"`
			File        string    `json:"file"`
			RotationPyr []float64 `json:"rotation_pyr"`
		} `json:"cells"`
		Dirtied []string `json:"dirtied"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, err
	}
	frames := make([]captureFrame, len(pr.Cells))
	for i, cell := range pr.Cells {
		yaw := 0.0
		if len(cell.RotationPyr) > 1 {
			yaw = cell.RotationPyr[1]
		}
		frames[i] = captureFrame{Index: cell.Index, File: cell.File, State: map[string]any{"yaw": yaw}}
	}
	png, sidecar, err := montage(pr.Dir, frames, orDefaultInt(in.Cols, 4), false, nil)
	if err != nil {
		return nil, err
	}
	sidecar["center"], sidecar["angles"], sidecar["dirtied"] = comb["origin"], num, pr.Dirtied
	return &spec.Result{Data: sidecar, Content: []mcp.Content{&mcp.ImageContent{Data: png, MIMEType: "image/png"}},
		Summary: fmt.Sprintf("%d-angle contact sheet", num)}, nil
}

func anyVec3(v any) [3]float64 {
	var out [3]float64
	if s, ok := v.([]any); ok {
		for i := 0; i < 3 && i < len(s); i++ {
			out[i], _ = s[i].(float64)
		}
	}
	return out
}

// --- capture ---------------------------------------------------------------------

type captureIn struct {
	Op          string    `json:"op" jsonschema:"start | status | stop | read | clear"`
	Session     string    `json:"session,omitempty" jsonschema:"start: a session id (default generated); status/stop/read/clear: the session"`
	World       string    `json:"world,omitempty" jsonschema:"start: editor (default) | pie"`
	Source      string    `json:"source,omitempty" jsonschema:"start: scene_capture (default; editor/simulate, works backgrounded) | pie_highres (possessed PIE, needs a visible viewport) | game_scene (live PIE via the UnrealMCP plugin, works backgrounded)"`
	IntervalS   float64   `json:"interval_s,omitempty" jsonschema:"start: seconds between frames (default 0.25)"`
	CellWidth   int       `json:"cell_width,omitempty" jsonschema:"start: frame width (default 480)"`
	CellHeight  int       `json:"cell_height,omitempty" jsonschema:"start: frame height (default 270)"`
	CameraMode  string    `json:"camera_mode,omitempty" jsonschema:"start: viewport (default; follow the editor camera) | fixed | actor"`
	CameraActor string    `json:"camera_actor,omitempty" jsonschema:"start camera_mode=actor: the actor label to ride"`
	CameraFov   float64   `json:"camera_fov,omitempty" jsonschema:"start source=game_scene: field of view (default 90)"`
	Location    []float64 `json:"location,omitempty" jsonschema:"start camera_mode=fixed: [x, y, z]"`
	Rotation    []float64 `json:"rotation,omitempty" jsonschema:"start camera_mode=fixed: [pitch, yaw, roll]"`
	TrackActors []string  `json:"track_actors,omitempty" jsonschema:"start: actor labels to record per-frame state for"`
	MaxFrames   int       `json:"max_frames,omitempty" jsonschema:"start: auto-stop after this many frames (default 240)"`
	MaxSeconds  float64   `json:"max_seconds,omitempty" jsonschema:"start: auto-stop after this many seconds (default 60)"`
	Include     []string  `json:"include,omitempty" jsonschema:"start: observed property include globs"`
	Exclude     []string  `json:"exclude,omitempty" jsonschema:"start: observed property exclude globs"`
	Properties  []string  `json:"properties,omitempty" jsonschema:"start: exact observed properties"`
	IncludeUI   bool      `json:"include_ui,omitempty" jsonschema:"start source=game_scene: include the HUD (needs a visible window)"`
	Cols        int       `json:"cols,omitempty" jsonschema:"stop/read: contact-sheet columns (default 8 / 6)"`
	DrawLabels  bool      `json:"draw_labels,omitempty" jsonschema:"stop: stamp each frame's world time"`
	MarkCells   []int     `json:"mark_cells,omitempty" jsonschema:"stop: frame indices to outline in red"`
	Path        string    `json:"path,omitempty" jsonschema:"read: a directory of frames on disk instead of a session"`
	Glob        string    `json:"glob,omitempty" jsonschema:"read: file pattern within the directory (default *)"`
	All         bool      `json:"all,omitempty" jsonschema:"clear: delete EVERY MCP capture (instead of one session)"`
}

func captureSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "start", Summary: "start an in-editor frame + state recorder", Tier: spec.Ephemeral, Reaches: []string{"capture_start"},
			Rejects: []string{"cols", "draw_labels", "mark_cells", "path", "glob", "all"}},
		{Name: "status", Summary: "frames so far (does not block)", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"session"}, Reaches: []string{"capture_poll"}},
		{Name: "stop", Summary: "stop; contact sheet + timeline", Tier: spec.Ephemeral, Required: []string{"session"}, Reaches: []string{"capture_stop"}},
		{Name: "read", Summary: "contact sheet of frames already on disk", Tier: spec.ReadOnly, Idempotent: true},
		{Name: "clear", Summary: "delete capture files (server-owned dirs only)", Tier: spec.Ephemeral, Idempotent: true},
	}
	return &spec.Spec{
		Name: "capture", Title: "Record frames", Toolset: spec.Core, Timeout: sync20, Max: sync28, Ops: ops,
		Description: "Film the world: an in-editor recorder saves a frame + observed state every interval_s with no per-frame " +
			"round trip.\n" +
			"- op=start → session.  - op=status: frames so far.\n" +
			"- op=stop: ONE contact-sheet image + a timeline (per-frame world time, state, cell).\n" +
			"- op=read: a contact sheet of a past session or a `path` of frames.\n" +
			"- op=clear: delete one session's files, or all=true for every MCP capture (Saved/MCP/capture only).",
		Schema: spec.SchemaFor[captureIn](map[string][]any{"op": spec.OpEnum(ops...), "world": {"editor", "pie"},
			"source": {"scene_capture", "pie_highres", "game_scene"}, "camera_mode": {"viewport", "fixed", "actor"}}, "op"),
		Replaces: []string{"capture_start", "capture_status", "capture_stop", "capture_clear", "read_capture"},
		Handler:  captureHandler,
	}
}

func captureHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in captureIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.Session != "" && !snapshot.ValidName(in.Session) {
		return nil, envelope.New(envelope.InvalidArgument, "session %q: use 1-64 letters, digits, '_' or '-'", in.Session)
	}
	switch c.Op.Name {
	case "start":
		args := pick(c.Args, "session", "source", "interval_s", "cell_width", "cell_height", "max_frames", "max_seconds", "include_ui", "track_actors")
		args["world"] = orStr(in.World, "editor")
		cam := rename(map[string]any{}, c.Args, "camera_mode", "mode", "camera_actor", "actor_label", "location", "location", "rotation", "rotation_pyr", "camera_fov", "fov")
		if len(cam) > 0 {
			args["camera"] = cam
		}
		if obs := pick(c.Args, "include", "exclude", "properties"); len(obs) > 0 {
			args["observe"] = obs
		}
		out, err := v2Op(ctx, c, "capture_start", args)
		return &spec.Result{Data: out, Summary: fmt.Sprintf("recording session %v", out["session"])}, err
	case "status":
		out, err := v2Op(ctx, c, "capture_poll", map[string]any{"session": in.Session})
		return &spec.Result{Data: out, Summary: fmt.Sprintf("%v frames", out["frames_captured"])}, err
	case "stop":
		b, err := v2Bridge(c)
		if err != nil {
			return nil, err
		}
		raw, err := b.Call(ctx, "capture_stop", map[string]any{"session": in.Session})
		if err != nil {
			return nil, err
		}
		var r captureStopResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if len(r.Frames) == 0 {
			return nil, envelope.New(envelope.OperationFailed, "the capture produced no frames").
				WithHint("the recorder saw no ticks — was the editor/PIE ticking (not minimized or paused)?")
		}
		png, sidecar, err := montage(r.Dir, r.Frames, orDefaultInt(in.Cols, 8), in.DrawLabels, in.MarkCells)
		if err != nil {
			return nil, err
		}
		sidecar["session"], sidecar["stop_reason"] = in.Session, r.StopReason
		return &spec.Result{Data: sidecar, Content: []mcp.Content{&mcp.ImageContent{Data: png, MIMEType: "image/png"}},
			Summary: fmt.Sprintf("%d frames (%s)", r.FrameCount, r.StopReason)}, nil
	case "read":
		return captureRead(c, in)
	}
	return captureClear(c, in)
}

func captureRoot(c *spec.Call) (string, error) {
	dir, err := projectDir(c)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Saved", "MCP", "capture"), nil
}

func captureRead(c *spec.Call, in captureIn) (*spec.Result, error) {
	dir := in.Path
	if dir == "" {
		if in.Session == "" {
			return nil, envelope.New(envelope.InvalidArgument, "capture op=read needs session or path")
		}
		root, err := captureRoot(c)
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(root, in.Session)
	}
	matches, err := filepath.Glob(filepath.Join(dir, orStr(in.Glob, "*")))
	if err != nil {
		return nil, envelope.New(envelope.InvalidArgument, "glob: %v", err)
	}
	var frames []captureFrame
	for _, m := range matches {
		if filepath.Ext(m) == ".json" {
			continue // manifest
		}
		if fi, err := os.Stat(m); err == nil && !fi.IsDir() && fi.Size() > 0 {
			frames = append(frames, captureFrame{Index: len(frames), File: filepath.Base(m)})
		}
	}
	if len(frames) == 0 {
		return nil, envelope.New(envelope.NotFound, "no frames in %s", dir)
	}
	png, sidecar, err := montage(dir, frames, orDefaultInt(in.Cols, 6), false, nil)
	if err != nil {
		return nil, err
	}
	return &spec.Result{Data: sidecar, Content: []mcp.Content{&mcp.ImageContent{Data: png, MIMEType: "image/png"}},
		Summary: fmt.Sprintf("%d frames from %s", len(frames), dir)}, nil
}

func captureClear(c *spec.Call, in captureIn) (*spec.Result, error) {
	if (in.Session == "") == !in.All {
		return nil, envelope.New(envelope.InvalidArgument, "capture op=clear needs exactly one of session or all=true")
	}
	root, err := captureRoot(c)
	if err != nil {
		return nil, err
	}
	shots := filepath.Join(filepath.Dir(filepath.Dir(root)), "Screenshots") // Saved/Screenshots (pie_highres frames)
	var targets []string
	if in.Session != "" {
		targets = append(targets, filepath.Join(root, in.Session))
		frame := regexp.MustCompile(`^mcp_` + regexp.QuoteMeta(in.Session) + `_f\d+\.png$`) // not session "a_b"'s files
		ms, _ := filepath.Glob(filepath.Join(shots, "mcp_"+in.Session+"_*"))
		for _, m := range ms {
			if frame.MatchString(filepath.Base(m)) {
				targets = append(targets, m)
			}
		}
	} else {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			targets = append(targets, filepath.Join(root, e.Name()))
		}
		ms, _ := filepath.Glob(filepath.Join(shots, "mcp_*"))
		targets = append(targets, ms...)
	}
	removed := 0
	for _, t := range targets {
		if _, err := os.Lstat(t); err == nil && os.RemoveAll(t) == nil {
			removed++
		}
	}
	return &spec.Result{Data: map[string]any{"removed": removed, "capture_dir": root}, Summary: fmt.Sprintf("removed %d capture entries", removed)}, nil
}

// --- scene / scene_clear ---------------------------------------------------------

type sceneIn struct {
	Op        string            `json:"op" jsonschema:"apply | check | preview | env_preset"`
	Path      string            `json:"path,omitempty" jsonschema:"apply/check: an unreal.scene/v1 spec file"`
	JSON      string            `json:"json,omitempty" jsonschema:"apply/check: the spec as inline JSON (instead of path)"`
	DryRun    bool              `json:"dry_run,omitempty" jsonschema:"apply: report add/update/missing assets without changing the level"`
	Save      *bool             `json:"save,omitempty" jsonschema:"apply/env_preset: save afterwards (default true)"`
	Checks    *scenespec.Checks `json:"checks,omitempty" jsonschema:"check: invariants to require (default: lit, no missing meshes, a PlayerStart)"`
	Layout    *scenespec.Layout `json:"layout,omitempty" jsonschema:"preview: {type: grid|ring|line|scatter, ...}"`
	Preset    string            `json:"preset,omitempty" jsonschema:"env_preset: daytime_clear | overcast | dusk | night | studio"`
	Overrides map[string]any    `json:"overrides,omitempty" jsonschema:"env_preset: e.g. {sun_rotation_pyr: [-45, 30, 0], sun_intensity_lux: 75000, exposure_ev100: 11}"`
	SceneID   string            `json:"scene_id,omitempty" jsonschema:"env_preset: scene id for the environment actors (default env)"`
}

func sceneSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "apply", Summary: "realize a scene spec (additive; one undo step)", Tier: spec.Mutating, Idempotent: true, Rejects: []string{"prune"}, Reaches: []string{"scene_apply", "scene_actors"}},
		{Name: "check", Summary: "lint the level's design invariants", Tier: spec.ReadOnly, Idempotent: true, Reaches: []string{"design_probe"}},
		{Name: "preview", Summary: "where a layout would place instances (offline)", Tier: spec.ReadOnly, Idempotent: true, Required: []string{"layout"}},
		{Name: "env_preset", Summary: "apply a lighting/sky/exposure preset", Tier: spec.Mutating, Idempotent: true, Required: []string{"preset"}, Reaches: []string{"scene_apply"}},
	}
	return &spec.Spec{
		Name: "scene", Title: "Declarative scenes", Toolset: spec.Core, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Build levels from a declarative unreal.scene/v1 spec (`path` or `json`).\n" +
			"- op=apply: create/update the spec's actors in one undo step and save; dry_run=true reports the diff only. " +
			"ADDITIVE: actors are matched by the scene's tag (mcp_scene:<id>), never by label alone, and nothing is deleted " +
			"(remove stale ones with scene_clear op=prune).\n" +
			"- op=check: lint the level (lit, no missing meshes, PlayerStart, nav bounds).\n" +
			"- op=preview: offline — where a layout would place things.\n" +
			"- op=env_preset: lighting/sky/exposure preset as scene actors (the sun always points down).",
		Schema: spec.SchemaFor[sceneIn](map[string][]any{"op": spec.OpEnum(ops...),
			"preset": {"daytime_clear", "overcast", "dusk", "night", "studio"}}, "op"),
		Replaces: []string{"scene_apply", "scene_plan", "design_check", "layout_preview", "env_preset_apply"},
		Handler:  sceneHandler,
	}
}

// loadScene parses and compiles a spec from path/json; diagnostics with errors are
// an INVALID_ARGUMENT carrying them.
func loadScene(path, inline string) (*scenespec.Spec, scenespec.Plan, []map[string]any, error) {
	var data []byte
	switch {
	case path != "" && inline != "":
		return nil, scenespec.Plan{}, nil, envelope.New(envelope.InvalidArgument, "give the spec as path or json, not both")
	case path != "":
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, scenespec.Plan{}, nil, envelope.New(envelope.NotFound, "spec file: %v", err)
		}
		data = b
	case inline != "":
		data = []byte(inline)
	default:
		return nil, scenespec.Plan{}, nil, envelope.New(envelope.InvalidArgument, "the spec is required: path or json")
	}
	sp, diags, err := scenespec.Parse(data)
	if err != nil {
		return nil, scenespec.Plan{}, nil, envelope.New(envelope.InvalidArgument, "parse spec: %v", err)
	}
	plan, cdiags := scenespec.Compile(sp)
	diags = append(diags, cdiags...)
	if hasErrorDiag(diags) {
		return nil, plan, nil, envelope.New(envelope.InvalidArgument, "the spec has errors; nothing was applied").WithDetail("diagnostics", diagsToJSON(diags))
	}
	return sp, plan, diagsToJSON(diags), nil
}

func sceneHandler(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in sceneIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	switch c.Op.Name {
	case "preview":
		pts := scenespec.Expand(*in.Layout, scenespec.Transform{Scale: [3]float64{1, 1, 1}})
		out := make([]map[string]any, len(pts))
		for i, p := range pts {
			out[i] = map[string]any{"location": p.Location, "rotation": p.RotationPyr}
		}
		return &spec.Result{Data: map[string]any{"count": len(out), "instances": out}, Summary: fmt.Sprintf("%d placements", len(out))}, nil
	case "check":
		checks := scenespec.Checks{RequireEnvironmentLit: true, RequireNoMissingMeshes: true, RequirePlayerStart: true}
		if in.Checks != nil {
			checks = *in.Checks
		} else if in.Path != "" || in.JSON != "" {
			sp, _, _, err := loadScene(in.Path, in.JSON)
			if err != nil {
				return nil, err
			}
			checks = sp.Checks
		}
		b, err := v2Bridge(c)
		if err != nil {
			return nil, err
		}
		raw, err := b.Call(ctx, "design_probe", map[string]any{})
		if err != nil {
			return nil, err
		}
		var facts scenespec.ProbeFacts
		if err := json.Unmarshal(raw, &facts); err != nil {
			return nil, err
		}
		rep := scenespec.EvaluateChecks(checks, facts)
		return &spec.Result{Data: map[string]any{"ok": rep.OK, "results": rep.Results}, Summary: map[bool]string{true: "all checks pass", false: "checks failed"}[rep.OK]}, nil
	case "env_preset":
		sp := &scenespec.Spec{Schema: "unreal.scene/v1", SceneID: orStr(in.SceneID, "env"),
			Environment: &scenespec.Environment{Preset: in.Preset, Overrides: in.Overrides}}
		plan, diags := scenespec.Compile(sp)
		if hasErrorDiag(diags) {
			return nil, envelope.New(envelope.InvalidArgument, "the preset produced errors; nothing was applied").WithDetail("diagnostics", diagsToJSON(diags))
		}
		return sceneApplyPlan(ctx, c, plan, in.Save, diagsToJSON(diags))
	}
	sp, plan, diags, err := loadScene(in.Path, in.JSON)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		before, err := sceneActors(ctx, c, sp.SceneID)
		if err != nil {
			return nil, err
		}
		d := scenespec.Diff(sp.SceneID, plan, before)
		return &spec.Result{Data: map[string]any{"dry_run": true, "scene_id": sp.SceneID, "diff": d, "diagnostics": diags, "placements": len(plan.Placements)},
			Summary: fmt.Sprintf("would add %d, update %d", len(d.Add), len(d.Update))}, nil
	}
	return sceneApplyPlan(ctx, c, plan, in.Save, diags)
}

// sceneActors is the scene's tagged actors by label (what apply would update).
func sceneActors(ctx context.Context, c *spec.Call, sceneID string) (map[string]scenespec.SnapActor, error) {
	out, err := v2Op(ctx, c, "scene_actors", map[string]any{"scene_id": sceneID})
	if err != nil {
		return nil, err
	}
	res := map[string]scenespec.SnapActor{}
	list, _ := out["actors"].([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		label, _ := m["label"].(string)
		class, _ := m["class"].(string)
		res[label] = scenespec.SnapActor{Location: anyVec3(m["location"]), Class: class}
	}
	return res, nil
}

func sceneApplyPlan(ctx context.Context, c *spec.Call, plan scenespec.Plan, save *bool, diags []map[string]any) (*spec.Result, error) {
	args := map[string]any{"scene_id": plan.SceneID, "placements": plan.Placements}
	if save != nil {
		args["save"] = *save
	}
	out, err := v2Op(ctx, c, "scene_apply", args)
	if err != nil {
		return nil, err
	}
	if len(diags) > 0 {
		out["diagnostics"] = diags
	}
	if errs, _ := out["errors"].([]any); len(errs) > 0 {
		// Hard placement failures: nothing was saved. Partial content may exist (one undo step).
		return nil, withLog(envelope.New(envelope.OperationFailed, "%d placement(s) failed; the level was not saved", len(errs)).
			WithDetail("result", out), out)
	}
	return &spec.Result{Data: out, Summary: fmt.Sprintf("scene %s: %v spawned, %v updated", plan.SceneID, out["spawned"], out["updated"])}, nil
}

type sceneClearIn struct {
	Op      string `json:"op" jsonschema:"all | prune"`
	SceneID string `json:"scene_id,omitempty" jsonschema:"all: the scene whose actors to delete"`
	Path    string `json:"path,omitempty" jsonschema:"prune: the scene spec file (its scene_id and labels are kept)"`
	JSON    string `json:"json,omitempty" jsonschema:"prune: the spec as inline JSON"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"list what would be deleted without deleting"`
	Save    *bool  `json:"save,omitempty" jsonschema:"save afterwards (default true)"`
}

func sceneClearSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "all", Summary: "delete every actor of a scene", Tier: spec.Destructive, Required: []string{"scene_id"}, Rejects: []string{"path", "json"}, Reaches: []string{"scene_clear", "scene_actors"}},
		{Name: "prune", Summary: "delete the scene's actors that are not in the spec", Tier: spec.Destructive, Rejects: []string{"scene_id"}, Reaches: []string{"scene_prune", "scene_actors"}},
	}
	return &spec.Spec{
		Name: "scene_clear", Title: "Remove scene actors", Toolset: spec.Core, Timeout: sync25, Max: sync28, Ops: ops,
		Description: "Delete actors a scene created (only actors tagged mcp_scene:<id>; hand-placed actors are never touched), " +
			"as one undo step, then save.\n" +
			"- op=all: every actor of `scene_id`.\n" +
			"- op=prune: the scene's actors whose label is not in the spec (`path` or `json`) — run after scene apply " +
			"to drop stale ones. A separate step from apply, not atomic with it.\n" +
			"dry_run=true lists what would go.",
		Schema:   spec.SchemaFor[sceneClearIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces: []string{"scene_clear"},
		Handler:  sceneClear,
	}
}

func sceneClear(ctx context.Context, c *spec.Call) (*spec.Result, error) {
	var in sceneClearIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	sceneID, keep := in.SceneID, map[string]bool{}
	if c.Op.Name == "prune" {
		sp, plan, _, err := loadScene(in.Path, in.JSON)
		if err != nil {
			return nil, err
		}
		sceneID = sp.SceneID
		for _, p := range plan.Placements {
			keep[p.Label] = true
		}
	}
	if in.DryRun {
		cur, err := sceneActors(ctx, c, sceneID)
		if err != nil {
			return nil, err
		}
		would := []string{}
		for label := range cur {
			if !keep[label] {
				would = append(would, label)
			}
		}
		sort.Strings(would)
		return &spec.Result{Data: map[string]any{"dry_run": true, "scene_id": sceneID, "would_delete": would},
			Summary: fmt.Sprintf("would delete %d actors", len(would))}, nil
	}
	args := map[string]any{"scene_id": sceneID}
	if in.Save != nil {
		args["save"] = *in.Save
	}
	if c.Op.Name == "all" {
		out, err := v2Op(ctx, c, "scene_clear", args)
		return &spec.Result{Data: out, Summary: fmt.Sprintf("removed %v actors of %s", out["removed"], sceneID)}, err
	}
	labels := make([]string, 0, len(keep))
	for l := range keep {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	args["keep"] = labels
	out, err := v2Op(ctx, c, "scene_prune", args)
	if err != nil {
		return nil, err
	}
	pruned, _ := out["pruned"].([]any)
	return &spec.Result{Data: out, Summary: fmt.Sprintf("pruned %d actors of %s", len(pruned), sceneID)}, nil
}

// --- audio -----------------------------------------------------------------------

type audioIn struct {
	Op      string  `json:"op" jsonschema:"capture_start | capture_stop | play"`
	Session string  `json:"session,omitempty" jsonschema:"capture_start: a session name"`
	Sound   string  `json:"sound,omitempty" jsonschema:"play: a sound asset path"`
	Volume  float64 `json:"volume,omitempty" jsonschema:"play: volume multiplier (default 1)"`
}

func audioSpec() *spec.Spec {
	ops := []spec.OpSpec{
		{Name: "capture_start", Summary: "tap the main submix (RMS/peak envelope)", Tier: spec.Ephemeral, Reaches: []string{"audio_capture_start"}},
		{Name: "capture_stop", Summary: "stop; write the envelope (Saved/MCP/audio)", Tier: spec.Ephemeral, Reaches: []string{"audio_capture_stop"}},
		{Name: "play", Summary: "play a sound into the game (a test signal)", Tier: spec.Ephemeral, Required: []string{"sound"}, Reaches: []string{"play_test_sound"}},
	}
	return &spec.Spec{
		Name: "audio", Title: "Game audio", Toolset: spec.Core, Timeout: sync15, Max: sync28, Ops: ops,
		Description: "Listen to the running game (PIE only; audio renders only in play; needs the UnrealMCP plugin).\n" +
			"- op=capture_start: record the main submix's RMS/peak envelope.\n" +
			"- op=capture_stop: stop → {path, points, max_rms, duration} (feeds design_audit kind=audio_audit).\n" +
			"- op=play: play `sound` as a known test signal.",
		Schema:   spec.SchemaFor[audioIn](map[string][]any{"op": spec.OpEnum(ops...)}, "op"),
		Replaces: []string{"audio_capture_start", "audio_capture_stop", "play_test_sound"},
		Handler: func(ctx context.Context, c *spec.Call) (*spec.Result, error) {
			py := map[string]string{"capture_start": "audio_capture_start", "capture_stop": "audio_capture_stop", "play": "play_test_sound"}[c.Op.Name]
			out, err := v2Op(ctx, c, py, pick(c.Args, "session", "sound", "volume"))
			return &spec.Result{Data: out, Summary: "audio " + c.Op.Name}, err
		},
	}
}

// awaitShot finds the file HighResShot actually wrote for `want`: UE puts a bare name
// under GameScreenshotSaveDirectory (Saved/Screenshots/<Platform>/) and may add a
// suffix, so look for <stem>*.png in the directory and its subdirectories.
func awaitShot(want string, wait time.Duration) string {
	dir, stem := filepath.Dir(want), strings.TrimSuffix(filepath.Base(want), ".png")
	deadline := time.Now().Add(wait)
	for {
		for _, pat := range []string{filepath.Join(dir, stem+"*.png"), filepath.Join(dir, "*", stem+"*.png")} {
			if ms, _ := filepath.Glob(pat); len(ms) > 0 {
				return ms[0]
			}
		}
		if time.Now().After(deadline) {
			return want
		}
		time.Sleep(200 * time.Millisecond)
	}
}
