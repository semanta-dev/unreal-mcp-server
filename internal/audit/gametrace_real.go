package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
)

// LoadTimeline reads a per-frame capture timeline (JSONL) such as PolyWorld's
// Saved/PolySlice/slice_timeline.jsonl. Known keys (tag/seq/simTime/static) map to
// the struct fields; every other numeric key is preserved in Extra. Used by the
// real-fixture acceptance tests that assert the audits fail the actual shipped
// PolyWorld defects (static:0 every frame; primaryLane never flips).
func LoadTimeline(path string) ([]TimelineSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []TimelineSample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var raw map[string]any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		ts := TimelineSample{Extra: map[string]float64{}}
		for k, v := range raw {
			num, isNum := v.(json.Number)
			switch k {
			case "tag":
				if s, ok := v.(string); ok {
					ts.Tag = s
				}
			case "seq":
				if isNum {
					if n, e := num.Int64(); e == nil {
						ts.Seq = int(n)
					}
				}
			case "simTime", "sim_time":
				if isNum {
					if fl, e := num.Float64(); e == nil {
						ts.SimTime = fl
					}
				}
			case "static":
				if isNum {
					if n, e := num.Int64(); e == nil {
						ts.Static = int(n)
					}
				}
			default:
				if isNum {
					if fl, e := num.Float64(); e == nil {
						ts.Extra[k] = fl
					}
				}
			}
		}
		out = append(out, ts)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
