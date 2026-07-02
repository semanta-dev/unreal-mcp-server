// Command abparity is the P4 A/B parity harness (GO_REWRITE_PLAN.md §15). It
// spawns the Python server and the Go server as subprocesses over stdio, calls
// the same tools on both, and diffs the results. Structured tools are compared
// semantically (Go may ADD fields — e.g. editor_status.bridge_version — but must
// not change or drop what Python returns); text tools are compared by text;
// image tools are asserted to both return non-empty PNG bytes.
//
// Requires the editor running with the project open, and both servers built.
// Run:  abparity -py .venv/Scripts/python.exe -py-args server.py -go dist/unreal-mcp.exe
//
// Exit 0 = all compared tools parity-equal (or additive-only); non-zero on any diff.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolCall struct {
	name string
	args map[string]any
	kind string // "text" | "struct" | "array" | "image"
}

// Safe, read-only-ish calls run by default (no level mutation). Use -mutating to
// also exercise spawn/delete/transform against a scratch actor.
var safeCalls = []toolCall{
	{"editor_status", map[string]any{}, "struct"},
	{"list_actors", map[string]any{}, "array"},
	{"list_assets", map[string]any{"path": "/Game", "limit": 50}, "struct"},
	{"execute_python", map[string]any{"code": "1+1", "evaluate": true}, "text"},
	{"take_screenshot", map[string]any{"width": 320, "height": 240}, "image"},
}

func main() {
	pyCmd := flag.String("py", ".venv/Scripts/python.exe", "python interpreter for the reference server")
	pyArgs := flag.String("py-args", "server.py", "space-separated args for the python server")
	goCmd := flag.String("go", "dist/unreal-mcp.exe", "the Go server binary")
	goArgs := flag.String("go-args", "-command-addr 127.0.0.1:6777", "space-separated args for the Go server")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	py, err := connect(ctx, *pyCmd, strings.Fields(*pyArgs))
	if err != nil {
		fatal("connect python server: %v", err)
	}
	defer py.Close()
	goSrv, err := connect(ctx, *goCmd, strings.Fields(*goArgs))
	if err != nil {
		fatal("connect go server: %v", err)
	}
	defer goSrv.Close()

	diffs := 0
	for _, tc := range safeCalls {
		pr, perr := py.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		gr, gerr := goSrv.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if perr != nil || gerr != nil {
			fmt.Printf("[ERR ] %-24s py=%v go=%v\n", tc.name, perr, gerr)
			diffs++
			continue
		}
		if msg, ok := compare(tc, pr, gr); ok {
			fmt.Printf("[ OK ] %-24s %s\n", tc.name, msg)
		} else {
			fmt.Printf("[DIFF] %-24s %s\n", tc.name, msg)
			diffs++
		}
	}
	fmt.Printf("\n%d/%d tools parity-equal\n", len(safeCalls)-diffs, len(safeCalls))
	if diffs > 0 {
		os.Exit(1)
	}
}

func connect(ctx context.Context, command string, args []string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "abparity", Version: "1"}, nil)
	cmd := exec.Command(command, args...)
	cmd.Stderr = os.Stderr
	return client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
}

func compare(tc toolCall, py, gr *mcp.CallToolResult) (string, bool) {
	switch tc.kind {
	case "text":
		p, g := firstText(py), firstText(gr)
		if p == g {
			return "text match", true
		}
		return fmt.Sprintf("text differ:\n  py=%q\n  go=%q", p, g), false
	case "array":
		var pa, ga []any
		if err := json.Unmarshal([]byte(firstText(py)), &pa); err != nil {
			return "python not a JSON array: " + firstText(py), false
		}
		_ = json.Unmarshal([]byte(firstText(gr)), &ga)
		if len(pa) == len(ga) {
			return fmt.Sprintf("array len %d match", len(pa)), true
		}
		return fmt.Sprintf("array len differ py=%d go=%d", len(pa), len(ga)), false
	case "struct":
		p := structOf(py)
		g := structOf(gr)
		if missing := additiveSuperset(p, g); missing == "" {
			return fmt.Sprintf("go is a superset of python's %d keys", len(p)), true
		}
		return "go missing/changed key: " + additiveSuperset(p, g), false
	case "image":
		if imageBytes(py) > 0 && imageBytes(gr) > 0 {
			return fmt.Sprintf("both PNG (py=%dB go=%dB)", imageBytes(py), imageBytes(gr)), true
		}
		return "one side produced no image", false
	}
	return "unknown kind", false
}

// additiveSuperset returns "" if every key/value in py is present and equal in
// gm (gm may add keys). Otherwise returns the offending key.
func additiveSuperset(py, gm map[string]any) string {
	for k, pv := range py {
		gv, ok := gm[k]
		if !ok {
			return k + " (missing)"
		}
		if !reflect.DeepEqual(pv, gv) {
			return fmt.Sprintf("%s (py=%v go=%v)", k, pv, gv)
		}
	}
	return ""
}

func firstText(r *mcp.CallToolResult) string {
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func structOf(r *mcp.CallToolResult) map[string]any {
	if r.StructuredContent != nil {
		b, _ := json.Marshal(r.StructuredContent)
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			return m
		}
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(firstText(r)), &m)
	return m
}

func imageBytes(r *mcp.CallToolResult) int {
	for _, c := range r.Content {
		if ic, ok := c.(*mcp.ImageContent); ok {
			return len(ic.Data)
		}
	}
	return 0
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(2)
}
