package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/desktop"
)

// sidecar decodes the JSON sidecar (the TextContent) from a shot result, after
// asserting the result carries exactly one image and one text part. Decoding
// (rather than matching the raw JSON) avoids false negatives from JSON escaping
// of '<' etc.
func sidecar(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	var img, txt int
	var text string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.ImageContent:
			img++
		case *mcp.TextContent:
			txt++
			text = v.Text
		}
	}
	if img != 1 {
		t.Fatalf("want exactly 1 image content, got %d", img)
	}
	if txt != 1 {
		t.Fatalf("want exactly 1 text sidecar, got %d", txt)
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("sidecar is not valid JSON: %v (%s)", err, text)
	}
	return m
}

// TestShotResultCoordHint guards the fix for the coord-mapping guidance: a
// window capture points the caller at window=<title>; a screen/region capture
// (no title) must tell the caller to add bounds.x/y for the absolute coordinate,
// and must NOT tell them to use a nonexistent window.
func TestShotResultCoordHint(t *testing.T) {
	// Window capture: titled, 1:1.
	win := &desktop.Shot{PNG: []byte("\x89PNGfake"), Width: 1286, Height: 726, SrcWidth: 1286, SrcHeight: 726,
		Bounds: desktop.Rect{X: 640, Y: 336, W: 1286, H: 726}, Method: "print", Title: "PolyWorld - Unreal Editor", PID: 51396}
	res, _, err := shotResult(win)
	if err != nil {
		t.Fatal(err)
	}
	hint, _ := sidecar(t, res)["coord_hint"].(string)
	if !strings.Contains(hint, "window=<title>") {
		t.Errorf("window capture hint should mention window=<title>: %s", hint)
	}
	if strings.Contains(hint, "bounds.x") {
		t.Errorf("window capture hint should NOT tell caller to add bounds: %s", hint)
	}

	// Screen capture on a secondary monitor: no title, downscaled, non-zero origin.
	scr := &desktop.Shot{PNG: []byte("\x89PNGfake"), Width: 900, Height: 506, SrcWidth: 2560, SrcHeight: 1440,
		Scaled: true, Bounds: desktop.Rect{X: 1920, Y: 0, W: 2560, H: 1440}, Method: "screen"}
	res2, _, err := shotResult(scr)
	if err != nil {
		t.Fatal(err)
	}
	m2 := sidecar(t, res2)
	hint2, _ := m2["coord_hint"].(string)
	if !strings.Contains(hint2, "bounds.x") || !strings.Contains(hint2, "bounds.y") {
		t.Errorf("screen capture hint must instruct adding bounds.x/y: %s", hint2)
	}
	if strings.Contains(hint2, "window=<title>") {
		t.Errorf("screen capture (no window) hint must NOT direct to window=<title>: %s", hint2)
	}
	if _, ok := m2["scale"]; !ok {
		t.Errorf("downscaled shot should include a scale factor: %v", m2)
	}
	if !strings.Contains(hint2, "downscaled") {
		t.Errorf("downscaled hint should mention downscaling: %s", hint2)
	}
}
