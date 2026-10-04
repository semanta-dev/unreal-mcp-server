package tools

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// pie op=aim is a closed loop over mouse-axis input. Each step reads the yaw/pitch the
// view must still turn (pie_aim_state), sends one tick of MouseX/MouseY sized by the
// gain measured so far (degrees of turn per axis unit: sensitivity, smoothing and the
// game's own scale folded together; the first step probes it), and learns the gain from
// the turn that tick produced. The view turns only through the player's input, as a
// mouse would: no rotation is ever set.

const (
	aimTolDeg   = 1.0
	aimMaxSteps = 12
	aimProbe    = 10.0   // axis units sent while a gain is unknown
	aimMaxUnits = 5000.0 // one tick's largest delta
	aimSettle   = 120 * time.Millisecond
)

var aimAxes = [2]struct{ key, errKey, angKey string }{
	{"MouseX", "yaw_error", "yaw"}, {"MouseY", "pitch_error", "pitch"},
}

// aimCal is what an aim measured: degrees per axis unit and the ticks one injection
// lasted, per axis.
type aimCal struct{ gain, ticks [2]float64 }

// aimGains remembers each project's calibration, so a later aim skips the probe.
var aimGains = struct {
	sync.Mutex
	m map[string]aimCal
}{m: map[string]aimCal{}}

func pieAim(ctx context.Context, c *spec.Call, in pieIn) (*spec.Result, error) {
	if (in.Actor == "") == (in.Class == "") {
		return nil, envelope.New(envelope.InvalidArgument, "aim needs actor or class (one of them)")
	}
	out, err := aimOnce(ctx, c, pick(c.Args, "actor", "class"))
	if err != nil {
		return nil, err
	}
	errs := [2]float64{out["yaw_error"].(float64), out["pitch_error"].(float64)}
	summary := fmt.Sprintf("aimed at %v (%.1f°, %.1f° off) in %d steps", out["target"], errs[0], errs[1], out["steps"])
	if out["aimed"] != true {
		summary = fmt.Sprintf("not on %v after %d steps: %.1f° yaw, %.1f° pitch off (a moving target: aim again)", out["target"], out["steps"], errs[0], errs[1])
	}
	return &spec.Result{Data: out, Summary: summary}, nil
}

// trackAim keeps aiming at the target (the nearest of a class: whichever is nearest
// now) until durationS has passed or the run ends; with no target in play it waits for
// one. A zero duration aims once.
func trackAim(ctx context.Context, c *spec.Call, target map[string]any, durationS float64, end time.Time) error {
	deadline := time.Now().Add(secs(durationS))
	if deadline.After(end) {
		deadline = end
	}
	for {
		_, err := aimOnce(ctx, c, target)
		if err != nil && envelope.Classify(err, false).Code != envelope.NotFound {
			return err
		}
		if durationS <= 0 {
			return err
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		pause := 60 * time.Millisecond // on target: re-check soon (it moves)
		if err != nil {
			pause = 300 * time.Millisecond // none in play yet
		}
		if sleepCtx(ctx, pause) != nil {
			return ctx.Err()
		}
	}
}

// aimOnce runs the loop once: {target, path, distance, aimed, yaw_error, pitch_error,
// steps, gain[, note]}.
func aimOnce(ctx context.Context, c *spec.Call, target map[string]any) (map[string]any, error) {
	aimGains.Lock()
	cal, ok := aimGains.m[c.Deps.ProjectDir]
	aimGains.Unlock()
	if !ok {
		cal.ticks = [2]float64{1, 1} // a 1 ms hold: one frame, normally; measured below
	}
	gain, ticks := cal.gain, cal.ticks
	var stuck [2]bool
	var trace []map[string]any // per step: axis units sent, degrees turned, error before
	st, err := v2Op(ctx, c, "pie_aim_state", target)
	if err != nil {
		return nil, err
	}
	step := 0
	for ; step < aimMaxSteps; step++ {
		errs := [2]float64{aimNum(st[aimAxes[0].errKey]), aimNum(st[aimAxes[1].errKey])}
		if aimDone(errs, stuck) {
			break
		}
		var sent [2]float64
		for i, ax := range aimAxes {
			if stuck[i] || math.Abs(errs[i]) <= aimTolDeg {
				continue
			}
			v := math.Copysign(aimProbe, errs[i])
			if gain[i] != 0 {
				// At most 90° a step: a turn near 180° reads back ambiguously (wrapped).
				turn := math.Max(-90, math.Min(90, errs[i]))
				v = math.Max(-aimMaxUnits, math.Min(aimMaxUnits, turn/gain[i]/ticks[i]))
			}
			if _, err := v2Op(ctx, c, "pie_input", map[string]any{"key": ax.key, "action": "axis", "value": v, "duration_s": 0.001}); err != nil {
				return nil, err
			}
			sent[i] = v
		}
		for i, ax := range aimAxes {
			if sent[i] != 0 {
				o := map[string]any{}
				awaitAxis(ctx, c, ax.key, 0.001, o)
				if o["done"] == true {
					sent[i] = aimNum(o["total"])
					if n := aimNum(o["ticks"]); n >= 1 {
						ticks[i] = n
					}
				}
			}
		}
		if err := sleepCtx(ctx, aimSettle); err != nil {
			return nil, err
		}
		next, err := v2Op(ctx, c, "pie_aim_state", target)
		if err != nil {
			return nil, err
		}
		var turnedAll [2]float64
		for i, ax := range aimAxes {
			turnedAll[i] = wrap180(aimNum(next[ax.angKey]) - aimNum(st[ax.angKey]))
		}
		trace = append(trace, map[string]any{"sent": sent, "turned": turnedAll, "error": errs})
		for i, ax := range aimAxes {
			if sent[i] == 0 {
				continue
			}
			turned := wrap180(aimNum(next[ax.angKey]) - aimNum(st[ax.angKey]))
			if math.Abs(turned) >= 179 {
				continue // ambiguous once wrapped: learn nothing from it
			}
			g := turned / sent[i]
			// No turn, or far less than (or against) a known gain predicts: the axis is
			// unbound, look input is ignored (the player died, a cursor mode), or pitch is
			// at its limit. Learning a gain from that would blow up the next step.
			if math.Abs(turned) < 0.01 || gain[i] != 0 && g/gain[i] < 0.25 {
				stuck[i] = true
				continue
			}
			if gain[i] == 0 {
				gain[i] = g
			} else {
				gain[i] = (gain[i] + g) / 2
			}
		}
		st = next
	}
	if stuck[0] && stuck[1] || stuck[0] && gain[0] == 0 {
		return nil, envelope.New(envelope.Precondition, "MouseX/MouseY input stopped turning the view (look_ignored: %v)", st["look_ignored"]).
			WithHint("is the player alive (game op=snapshot), look input bound to the mouse axes and not ignored (a cursor/tactical mode)? pie op=input action=axis key=MouseX shows what one axis does").
			WithDetail("state", st).WithDetail("trace", trace)
	}
	aimGains.Lock()
	aimGains.m[c.Deps.ProjectDir] = aimCal{gain, ticks}
	aimGains.Unlock()
	errs := [2]float64{aimNum(st[aimAxes[0].errKey]), aimNum(st[aimAxes[1].errKey])}
	aimed := math.Abs(errs[0]) <= aimTolDeg && math.Abs(errs[1]) <= aimTolDeg
	out := map[string]any{"target": st["target"], "path": st["path"], "distance": st["distance"], "aimed": aimed,
		"yaw_error": errs[0], "pitch_error": errs[1], "steps": step, "gain": map[string]any{"MouseX": gain[0], "MouseY": gain[1]}, "trace": trace}
	if stuck[1] && !aimed {
		out["note"] = "pitch stopped turning (at its limit?): the target may be out of the view's pitch range"
	}
	return out, nil
}

// aimDone: every axis is within tolerance or cannot turn further.
func aimDone(errs [2]float64, stuck [2]bool) bool {
	for i := range errs {
		if !stuck[i] && math.Abs(errs[i]) > aimTolDeg {
			return false
		}
	}
	return true
}

func wrap180(d float64) float64 {
	d = math.Mod(d+180, 360)
	if d < 0 {
		d += 360
	}
	return d - 180
}

func aimNum(v any) float64 {
	f, _ := v.(float64)
	return f
}
