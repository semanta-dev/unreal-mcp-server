package tools

import (
	"strings"
	"testing"

	"github.com/jdziat/unreal-mcp-server/internal/desktop"
)

// TestShotDataCoordHint: a window capture points the caller at window=<title>; a
// screen/region capture (no title) must say to add bounds.x/y, never a window.
func TestShotDataCoordHint(t *testing.T) {
	win := shotData(&desktop.Shot{Width: 1286, Height: 726, SrcWidth: 1286, SrcHeight: 726,
		Bounds: desktop.Rect{X: 640, Y: 336, W: 1286, H: 726}, Method: "print", Title: "PolyWorld - Unreal Editor", PID: 51396})
	if h := win["coord_hint"].(string); !strings.Contains(h, "window=<title>") || strings.Contains(h, "bounds.x") {
		t.Errorf("window capture hint: %s", h)
	}
	scr := shotData(&desktop.Shot{Width: 900, Height: 506, SrcWidth: 2560, SrcHeight: 1440, Scaled: true,
		Bounds: desktop.Rect{X: 1920, Y: 0, W: 2560, H: 1440}, Method: "screen"})
	h := scr["coord_hint"].(string)
	if !strings.Contains(h, "bounds.x") || !strings.Contains(h, "bounds.y") || strings.Contains(h, "window=<title>") || !strings.Contains(h, "downscaled") {
		t.Errorf("screen capture hint: %s", h)
	}
	if _, ok := scr["scale"]; !ok {
		t.Errorf("a downscaled shot needs a scale factor: %v", scr)
	}
}
