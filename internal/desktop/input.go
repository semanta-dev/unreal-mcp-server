package desktop

import (
	"fmt"
	"strings"
	"time"
)

// Windows mouse-event flags (winuser.h MOUSEEVENTF_*) and the wheel notch size.
// These are plain numeric constants (not syscalls) so they live in the base
// file; the platform backend passes them straight to SendInput.
const (
	mouseMove       = 0x0001
	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseWheel      = 0x0800
	mouseHWheel     = 0x1000
	wheelDelta      = 120
)

// MouseReq describes one mouse action.
type MouseReq struct {
	Action string      // move|click|double_click|down|up|drag|scroll
	X, Y   int         // target position
	ToX    int         // drag end X
	ToY    int         // drag end Y
	Button MouseButton // left|right|middle (default left)
	Amount int         // scroll notches (+up/right, -down/left); default 3
	Horiz  bool        // scroll horizontally
	Window Selector    // when non-empty, X/Y (and ToX/ToY) are relative to this window's top-left
	Steps  int         // interpolation steps for drag (default 12)
}

// buttonFlags returns the (down, up) MOUSEEVENTF flags for a button.
func buttonFlags(b MouseButton) (down, up uint32, err error) {
	switch b {
	case "", MouseLeft:
		return mouseLeftDown, mouseLeftUp, nil
	case MouseRight:
		return mouseRightDown, mouseRightUp, nil
	case MouseMiddle:
		return mouseMiddleDown, mouseMiddleUp, nil
	default:
		return 0, 0, fmt.Errorf("unknown mouse button %q (want left|right|middle)", b)
	}
}

// Point is a screen coordinate returned to the caller after an action.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Mouse performs a mouse action and returns the final cursor position. When
// req.Window is set, coordinates are interpreted relative to that window's
// top-left corner (the same origin as a window_capture image), so an agent can
// click a pixel it just saw without doing offset math.
func Mouse(req MouseReq) (Point, error) {
	ensureDPIAware()
	ox, oy := 0, 0
	if !req.Window.empty() {
		w, err := FindWindow(req.Window)
		if err != nil {
			return Point{}, err
		}
		ox, oy = w.X, w.Y
	}
	x, y := req.X+ox, req.Y+oy
	tox, toy := req.ToX+ox, req.ToY+oy

	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "move"
	}
	switch action {
	case "move":
		if err := moveCursor(x, y); err != nil {
			return Point{}, err
		}
	case "click", "double_click", "doubleclick", "double":
		down, up, err := buttonFlags(req.Button)
		if err != nil {
			return Point{}, err
		}
		if err := moveCursor(x, y); err != nil {
			return Point{}, err
		}
		clicks := 1
		if action != "click" {
			clicks = 2
		}
		for i := 0; i < clicks; i++ {
			if err := clickAt(down, up); err != nil {
				return Point{}, err
			}
			if i+1 < clicks {
				time.Sleep(60 * time.Millisecond)
			}
		}
	case "down", "press":
		down, _, err := buttonFlags(req.Button)
		if err != nil {
			return Point{}, err
		}
		if err := moveCursor(x, y); err != nil {
			return Point{}, err
		}
		if err := sendMouseEvent(down, 0); err != nil {
			return Point{}, err
		}
	case "up", "release":
		_, up, err := buttonFlags(req.Button)
		if err != nil {
			return Point{}, err
		}
		if err := moveCursor(x, y); err != nil {
			return Point{}, err
		}
		if err := sendMouseEvent(up, 0); err != nil {
			return Point{}, err
		}
	case "drag":
		if err := dragMouse(x, y, tox, toy, req.Button, req.Steps); err != nil {
			return Point{}, err
		}
		x, y = tox, toy
	case "scroll":
		if req.X != 0 || req.Y != 0 || !req.Window.empty() {
			if err := moveCursor(x, y); err != nil {
				return Point{}, err
			}
		}
		amount := req.Amount
		if amount == 0 {
			amount = 3
		}
		flag := uint32(mouseWheel)
		if req.Horiz {
			flag = mouseHWheel
		}
		if err := sendMouseEvent(flag, int32(amount*wheelDelta)); err != nil {
			return Point{}, err
		}
	default:
		return Point{}, fmt.Errorf("unknown mouse action %q", action)
	}
	cx, cy := getCursorPos()
	return Point{X: cx, Y: cy}, nil
}

// clickAt presses and releases a button at the current cursor position.
func clickAt(down, up uint32) error {
	if err := sendMouseEvent(down, 0); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	return sendMouseEvent(up, 0)
}

// dragMouse presses a button at the start, glides through interpolated steps to
// the end (so drag-aware UI registers the motion), then releases.
func dragMouse(x1, y1, x2, y2 int, button MouseButton, steps int) error {
	down, up, err := buttonFlags(button)
	if err != nil {
		return err
	}
	if steps <= 0 {
		steps = 12
	}
	if err := moveCursor(x1, y1); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	if err := sendMouseEvent(down, 0); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	for i := 1; i <= steps; i++ {
		ix := x1 + (x2-x1)*i/steps
		iy := y1 + (y2-y1)*i/steps
		if err := moveCursor(ix, iy); err != nil {
			_ = sendMouseEvent(up, 0)
			return err
		}
		time.Sleep(12 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	return sendMouseEvent(up, 0)
}

// Keys synthesizes a sequence of key chords (e.g. ["ctrl+s", "enter"]).
func Keys(chords []string) error {
	ensureDPIAware()
	events, err := chordSequence(chords)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("no keys to press")
	}
	return sendKeyEvents(events)
}

// TypeText types a Unicode string as literal characters (KEYEVENTF_UNICODE), so
// layout and modifiers do not matter — good for names, paths, and search boxes.
func TypeText(s string) error {
	ensureDPIAware()
	if s == "" {
		return fmt.Errorf("no text to type")
	}
	return typeUnicode(s)
}
