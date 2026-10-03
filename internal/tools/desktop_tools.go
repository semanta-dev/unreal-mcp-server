package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/desktop"
)

// registerDesktopTools adds OS-level screen capture and computer control:
// capturing the *actual on-screen* Unreal Editor window (Slate UI, dialogs,
// crash popups, the composited D3D viewport) and driving it with real mouse and
// keyboard input. This complements — it does not replace — the in-editor path
// (take_screenshot / capture_start / pie_input), which renders through Unreal's
// Python remote execution and only ever sees the 3D scene, never the editor's
// own UI. Backed by internal/desktop (raw Win32; Windows-only). These tools need
// no editor bridge, so they register unconditionally.
func registerDesktopTools(s *mcp.Server, _ Deps) {
	add(s, "list_windows",
		"List the OS's visible top-level windows (title, pid, handle, on-screen bounds, foreground/minimized). Use it to find the Unreal Editor window (or a dialog/crash popup) to capture or control. Optional case-insensitive title filter.",
		listWindows())

	add(s, "screen_capture",
		"Capture the actual display as a PNG — the whole virtual desktop (all monitors), one monitor, or a region. This is an OS screenshot of what is really on screen (unlike take_screenshot, which renders the 3D scene inside the editor). Downscaled to max_width for token economy.",
		screenCapture())

	add(s, "window_capture",
		"Capture a specific on-screen window as a PNG — defaults to auto-detecting the Unreal Editor. Sees the FULL editor: menus, panels, the Content Browser, modal dialogs, the composited viewport with UI. method=print (default) works even when the window is backgrounded/occluded and does not steal focus; method=screen blits its on-screen rectangle. Returns the window's screen bounds so you can map image pixels to mouse_control coordinates.",
		windowCapture())

	add(s, "focus_window",
		"Bring a window to the foreground (restoring it if minimized). Useful before a method=screen capture or before sending input that must land on a specific window. Selector: title substring, pid, or hwnd; default the Unreal Editor.",
		focusWindow())

	add(s, "mouse_control",
		"Drive the real mouse at the OS level: move|click|double_click|down|up|drag|scroll. Coordinates are screen pixels, OR set 'window' (a title substring like 'unreal') to make x/y relative to that window's top-left — the same origin as window_capture's image — so you can click exactly what you saw. Returns the final cursor position.",
		mouseControl())

	add(s, "key_press",
		"Send real keyboard shortcuts at the OS level: a chord like 'ctrl+s', 'F5', 'alt+f4', 'escape', 'delete', 'ctrl+shift+p', or a sequence of chords. Goes to the foreground window, so focus_window first if needed. For typing literal text use type_text.",
		keyPress())

	add(s, "type_text",
		"Type a literal Unicode string at the OS level into the focused control (KEYEVENTF_UNICODE — layout-independent). Use for search boxes, names, and paths; use key_press for shortcuts and navigation keys.",
		typeText())
}

// --- inputs ---

type listWindowsIn struct {
	Filter string `json:"filter,omitempty" jsonschema:"case-insensitive title substring to filter by; empty = all visible titled windows"`
}

type screenCaptureIn struct {
	Monitor  *int  `json:"monitor,omitempty" jsonschema:"monitor index (0-based, see list from MonitorRects); omit or -1 = whole virtual desktop spanning all monitors"`
	Region   []int `json:"region,omitempty" jsonschema:"[x,y,w,h] in virtual-desktop pixels to grab a sub-rectangle instead of a whole monitor"`
	MaxWidth *int  `json:"max_width,omitempty" jsonschema:"downscale so the image is at most this many px wide (aspect preserved); default 1600, 0 = full resolution"`
}

type windowSel struct {
	Title string `json:"title,omitempty" jsonschema:"window title substring (case-insensitive); omit to auto-detect the Unreal Editor"`
	Pid   int    `json:"pid,omitempty" jsonschema:"target a specific process id instead of by title"`
	Hwnd  uint64 `json:"hwnd,omitempty" jsonschema:"target an exact window handle (from list_windows)"`
}

func (w windowSel) selector() desktop.Selector {
	return desktop.Selector{HWND: uintptr(w.Hwnd), PID: w.Pid, Title: w.Title}
}

type windowCaptureIn struct {
	windowSel
	Method   string `json:"method,omitempty" jsonschema:"auto|print|screen. print (default) renders the window even when backgrounded/occluded and never steals focus; screen blits its on-screen rectangle (window must be visible)"`
	Focus    bool   `json:"focus,omitempty" jsonschema:"bring the window to the foreground before capturing"`
	MaxWidth *int   `json:"max_width,omitempty" jsonschema:"downscale to at most this width; default 1600, 0 = full resolution"`
}

type focusWindowIn struct {
	windowSel
}

type mouseControlIn struct {
	Action       string `json:"action" jsonschema:"move|click|double_click|down|up|drag|scroll"`
	X            int    `json:"x,omitempty" jsonschema:"target X (screen px, or window-relative when 'window'/'window_hwnd' is set)"`
	Y            int    `json:"y,omitempty" jsonschema:"target Y"`
	ToX          int    `json:"to_x,omitempty" jsonschema:"drag end X"`
	ToY          int    `json:"to_y,omitempty" jsonschema:"drag end Y"`
	Button       string `json:"button,omitempty" jsonschema:"left|right|middle; default left"`
	ScrollAmount int    `json:"scroll_amount,omitempty" jsonschema:"wheel notches for action=scroll; +up/right, -down/left; default 3"`
	Horizontal   bool   `json:"horizontal,omitempty" jsonschema:"scroll horizontally instead of vertically"`
	Window       string `json:"window,omitempty" jsonschema:"a window title substring (e.g. 'unreal') to interpret x/y (and to_x/to_y) RELATIVE to that window's top-left — the same origin as window_capture's image"`
	WindowHwnd   uint64 `json:"window_hwnd,omitempty" jsonschema:"like 'window' but an exact window handle"`
	Steps        int    `json:"steps,omitempty" jsonschema:"drag interpolation steps; default 12"`
}

type keyPressIn struct {
	Keys     string   `json:"keys,omitempty" jsonschema:"a single chord, e.g. 'ctrl+s', 'F5', 'alt+f4', 'escape', 'delete', 'ctrl+shift+p'"`
	Sequence []string `json:"sequence,omitempty" jsonschema:"a sequence of chords pressed in order, e.g. ['ctrl+a','delete']"`
}

type typeTextIn struct {
	Text string `json:"text" jsonschema:"the literal text to type as Unicode characters"`
}

// --- handlers ---

func listWindows() mcp.ToolHandlerFor[listWindowsIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listWindowsIn) (*mcp.CallToolResult, map[string]any, error) {
		wins, err := desktop.ListWindows(in.Filter)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"count": len(wins), "windows": wins}, nil
	}
}

func screenCapture() mcp.ToolHandlerFor[screenCaptureIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in screenCaptureIn) (*mcp.CallToolResult, any, error) {
		monitor := -1
		if in.Monitor != nil {
			monitor = *in.Monitor
		}
		var region *desktop.Rect
		if len(in.Region) == 4 {
			region = &desktop.Rect{X: in.Region[0], Y: in.Region[1], W: in.Region[2], H: in.Region[3]}
		} else if len(in.Region) != 0 {
			return nil, nil, fmt.Errorf("region must be [x,y,w,h] (4 ints), got %d", len(in.Region))
		}
		shot, err := desktop.CaptureScreen(monitor, region, maxWidthOr(in.MaxWidth))
		if err != nil {
			return nil, nil, err
		}
		return shotResult(shot)
	}
}

func windowCapture() mcp.ToolHandlerFor[windowCaptureIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in windowCaptureIn) (*mcp.CallToolResult, any, error) {
		shot, err := desktop.CaptureWindow(in.selector(), in.Method, in.Focus, maxWidthOr(in.MaxWidth))
		if err != nil {
			return nil, nil, err
		}
		return shotResult(shot)
	}
}

func focusWindow() mcp.ToolHandlerFor[focusWindowIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in focusWindowIn) (*mcp.CallToolResult, map[string]any, error) {
		w, err := desktop.FocusWindow(in.selector())
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"window": w, "foreground": w.Foreground}, nil
	}
}

func mouseControl() mcp.ToolHandlerFor[mouseControlIn, map[string]any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in mouseControlIn) (*mcp.CallToolResult, map[string]any, error) {
		req := desktop.MouseReq{
			Action: in.Action,
			X:      in.X, Y: in.Y, ToX: in.ToX, ToY: in.ToY,
			Button: desktop.MouseButton(strings.ToLower(in.Button)),
			Amount: in.ScrollAmount,
			Horiz:  in.Horizontal,
			Steps:  in.Steps,
		}
		if in.WindowHwnd != 0 {
			req.Window = desktop.Selector{HWND: uintptr(in.WindowHwnd)}
		} else if in.Window != "" {
			req.Window = desktop.Selector{Title: in.Window}
		}
		pt, err := desktop.Mouse(req)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"cursor": pt}, nil
	}
}

func keyPress() mcp.ToolHandlerFor[keyPressIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in keyPressIn) (*mcp.CallToolResult, any, error) {
		chords := in.Sequence
		if len(chords) == 0 && in.Keys != "" {
			chords = []string{in.Keys}
		}
		if len(chords) == 0 {
			return nil, nil, fmt.Errorf("provide keys or sequence")
		}
		if err := desktop.Keys(chords); err != nil {
			return nil, nil, err
		}
		return textResult("pressed " + strings.Join(chords, " ")), nil, nil
	}
}

func typeText() mcp.ToolHandlerFor[typeTextIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in typeTextIn) (*mcp.CallToolResult, any, error) {
		if err := desktop.TypeText(in.Text); err != nil {
			return nil, nil, err
		}
		return textResult(fmt.Sprintf("typed %d characters", len([]rune(in.Text)))), nil, nil
	}
}

// maxWidthOr resolves the optional max_width to its default (1600) while
// letting an explicit 0 mean "full resolution".
func maxWidthOr(p *int) int {
	if p == nil {
		return 1600
	}
	return *p
}

// shotResult returns a capture as inline image content plus a JSON metadata
// sidecar. The sidecar carries the source screen bounds and (when downscaled)
// the scale factor, so a caller can map a pixel it sees in the image back to
// the screen/window coordinates mouse_control expects.
func shotResult(shot *desktop.Shot) (*mcp.CallToolResult, any, error) {
	sidecar := map[string]any{
		"width": shot.Width, "height": shot.Height,
		"src_width": shot.SrcWidth, "src_height": shot.SrcHeight,
		"scaled": shot.Scaled, "method": shot.Method,
		"bounds": map[string]int{"x": shot.Bounds.X, "y": shot.Bounds.Y, "w": shot.Bounds.W, "h": shot.Bounds.H},
	}
	if shot.Title != "" {
		sidecar["title"] = shot.Title
	}
	if shot.PID != 0 {
		sidecar["pid"] = shot.PID
	}
	// coord_hint tells the caller how to turn an image pixel into a mouse_control
	// coordinate. A window capture's origin is the window's top-left, so the
	// convenience path is window=<title> (mouse_control adds the window offset).
	// A screen/region capture has NO window (title==""), and its origin is
	// bounds.x/y in the virtual desktop — which can be non-zero (secondary
	// monitor / region) or negative — so the pixel must be offset by bounds.x/y
	// to get an absolute screen coordinate.
	scaleNote := ""
	if shot.Scaled && shot.Width > 0 {
		sidecar["scale"] = float64(shot.SrcWidth) / float64(shot.Width)
		scaleNote = "Image is downscaled: first multiply the image pixel by 'scale' to get full-res source pixels. "
	}
	if shot.Title != "" {
		sidecar["coord_hint"] = scaleNote + "To click, pass window=<title> (or window_hwnd) to mouse_control with the full-res pixel as x/y — its origin is the window's top-left and mouse_control adds the window's screen offset for you."
	} else {
		sidecar["coord_hint"] = scaleNote + "Screen capture (no window): add bounds.x and bounds.y to the full-res pixel to get the absolute screen coordinate, then pass that as x/y to mouse_control with 'window' unset."
	}
	j, _ := json.Marshal(sidecar)
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: shot.PNG, MIMEType: "image/png"},
		&mcp.TextContent{Text: string(j)},
	}}, nil, nil
}
