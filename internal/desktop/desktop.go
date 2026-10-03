// Package desktop provides OS-level screen capture and computer control —
// capturing the *actual on-screen* Unreal Editor window (Slate UI, dialogs,
// crash popups, the composited viewport) and driving it with real mouse and
// keyboard input, the way a human operator would.
//
// This is deliberately distinct from the in-editor capture/input path
// (take_screenshot / capture_start / pie_input), which renders through
// Unreal's Python remote execution and only sees the 3D scene, never the
// editor's own UI. When you need to see what the editor genuinely looks like
// on the display — or click a menu, dismiss a modal, or press a global
// shortcut — you need OS-level capture and control. That is this package.
//
// The public surface is platform-agnostic; the heavy lifting lives in
// build-tagged files: capture_windows.go / input_windows.go / win_windows.go
// implement it with raw Win32 syscalls (GDI BitBlt/PrintWindow, SendInput,
// EnumWindows), matching the repo's dependency-light, hand-rolled-syscall
// culture. stub_other.go returns ErrUnsupported everywhere else so the linux
// CI (go test -race ./...) still builds. All pixel math, key parsing, window
// selection, and PNG encoding live here in pure Go and are unit-tested on
// every platform.
package desktop

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"time"
)

// ErrUnsupported is returned by every entry point on non-Windows platforms.
var ErrUnsupported = errors.New("desktop screen capture and control are only implemented on Windows")

// Rect is a screen rectangle in physical (DPI-aware) pixels. X/Y are the
// top-left corner in virtual-desktop coordinates, which can be negative on
// multi-monitor setups where a monitor sits left of / above the primary.
type Rect struct {
	X, Y, W, H int
}

func (r Rect) empty() bool { return r.W <= 0 || r.H <= 0 }

// Window describes one top-level window.
type Window struct {
	HWND       uintptr `json:"hwnd"`
	PID        int     `json:"pid"`
	Title      string  `json:"title"`
	X          int     `json:"x"`
	Y          int     `json:"y"`
	W          int     `json:"w"`
	H          int     `json:"h"`
	Minimized  bool    `json:"minimized"`
	Foreground bool    `json:"foreground"`
}

func (w Window) rect() Rect { return Rect{w.X, w.Y, w.W, w.H} }

// Selector picks a window. Resolution precedence: HWND, then PID, then Title
// substring (case-insensitive). When all are empty the Unreal Editor window is
// auto-detected (title contains "Unreal Editor", or failing that the process
// UnrealEditor.exe).
type Selector struct {
	HWND  uintptr
	PID   int
	Title string
}

func (s Selector) empty() bool { return s.HWND == 0 && s.PID == 0 && s.Title == "" }

// Shot is a captured image plus metadata about what/how it was captured.
type Shot struct {
	PNG       []byte `json:"-"`
	Width     int    `json:"width"` // final (possibly downscaled) pixels
	Height    int    `json:"height"`
	SrcWidth  int    `json:"src_width"` // captured pixels before any downscale
	SrcHeight int    `json:"src_height"`
	Scaled    bool   `json:"scaled"`
	Bounds    Rect   `json:"-"` // screen rect the pixels came from
	Method    string `json:"method,omitempty"`
	Title     string `json:"title,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

// MouseButton identifies which button an action uses.
type MouseButton string

const (
	MouseLeft   MouseButton = "left"
	MouseRight  MouseButton = "right"
	MouseMiddle MouseButton = "middle"
)

// ListWindows returns visible top-level windows with a non-empty title,
// optionally filtered to those whose title contains filter (case-insensitive).
func ListWindows(filter string) ([]Window, error) {
	ensureDPIAware()
	all, err := enumWindows()
	if err != nil {
		return nil, err
	}
	if filter == "" {
		return all, nil
	}
	needle := strings.ToLower(filter)
	out := make([]Window, 0, len(all))
	for _, w := range all {
		if strings.Contains(strings.ToLower(w.Title), needle) {
			out = append(out, w)
		}
	}
	return out, nil
}

// CloseWindow asks a window to close (WM_CLOSE), like clicking its title-bar X; a
// dialog treats that as Cancel.
func CloseWindow(hwnd uintptr) error { return closeHWND(hwnd) }

// FindWindow resolves a Selector to a single best-match window.
func FindWindow(sel Selector) (Window, error) {
	ensureDPIAware()
	all, err := enumWindows()
	if err != nil {
		return Window{}, err
	}
	w, ok := pickWindow(all, sel)
	if !ok {
		return Window{}, fmt.Errorf("no matching window for %s", describeSelector(sel))
	}
	return w, nil
}

// FocusWindow brings a window to the foreground (restoring it if minimized).
func FocusWindow(sel Selector) (Window, error) {
	w, err := FindWindow(sel)
	if err != nil {
		return Window{}, err
	}
	if err := focusHWND(w.HWND); err != nil {
		return w, err
	}
	// Re-read after the restore so bounds/foreground reflect the new state.
	if fresh, ok := windowByHWND(w.HWND); ok {
		return fresh, nil
	}
	w.Foreground = true
	return w, nil
}

// CaptureScreen captures a monitor (index into MonitorRects, 0-based), the
// whole virtual desktop (monitor < 0), or an explicit region. maxWidth
// downscales the result to at most that width for token economy (0 = full
// resolution). region, when non-nil, is in virtual-desktop pixels.
func CaptureScreen(monitor int, region *Rect, maxWidth int) (*Shot, error) {
	ensureDPIAware()
	var target Rect
	switch {
	case region != nil:
		target = *region
	case monitor < 0:
		vr, err := virtualScreenRect()
		if err != nil {
			return nil, err
		}
		target = vr
	default:
		mons, err := monitorRects()
		if err != nil {
			return nil, err
		}
		if monitor >= len(mons) {
			return nil, fmt.Errorf("monitor %d out of range (%d monitors)", monitor, len(mons))
		}
		target = mons[monitor]
	}
	if target.empty() {
		return nil, fmt.Errorf("capture region is empty: %+v", target)
	}
	img, err := captureRegionImg(target.X, target.Y, target.W, target.H)
	if err != nil {
		return nil, err
	}
	return finishShot(img, target, "", 0, "screen", maxWidth)
}

// CaptureWindow captures one window. method is auto|print|screen:
//   - print:  PrintWindow(PW_RENDERFULLCONTENT) — works even when the window is
//     backgrounded/occluded and does not steal focus (default).
//   - screen: BitBlt the window's on-screen rectangle — exactly what a human
//     sees (including the live D3D viewport), but the window must be visible.
//   - auto:   print, unless focus is requested (then screen after focusing).
//
// If focus is true the window is brought to the foreground first.
func CaptureWindow(sel Selector, method string, focus bool, maxWidth int) (*Shot, error) {
	ensureDPIAware()
	w, err := FindWindow(sel)
	if err != nil {
		return nil, err
	}
	if focus {
		if err := focusHWND(w.HWND); err != nil {
			return nil, fmt.Errorf("focus window: %w", err)
		}
		time.Sleep(120 * time.Millisecond) // let the compositor settle before capture
		if fresh, ok := windowByHWND(w.HWND); ok {
			w = fresh
		}
	}
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "", "auto":
		if focus {
			method = "screen"
		} else {
			method = "print"
		}
	case "print", "screen":
	default:
		return nil, fmt.Errorf("unknown capture method %q (want auto|print|screen)", method)
	}
	if w.Minimized && method == "screen" {
		return nil, fmt.Errorf("window is minimized; use method=print or focus=true")
	}
	img, err := captureWindowImg(w.HWND, method)
	if err != nil {
		return nil, err
	}
	return finishShot(img, w.rect(), w.Title, w.PID, method, maxWidth)
}

// finishShot downscales (if asked) and PNG-encodes a captured image.
func finishShot(img *image.RGBA, bounds Rect, title string, pid int, method string, maxWidth int) (*Shot, error) {
	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	scaled := false
	out := image.Image(img)
	if maxWidth > 0 && srcW > maxWidth {
		out = downscale(img, maxWidth)
		scaled = true
	}
	png, err := encodePNG(out)
	if err != nil {
		return nil, err
	}
	b := out.Bounds()
	return &Shot{
		PNG:       png,
		Width:     b.Dx(),
		Height:    b.Dy(),
		SrcWidth:  srcW,
		SrcHeight: srcH,
		Scaled:    scaled,
		Bounds:    bounds,
		Method:    method,
		Title:     title,
		PID:       pid,
	}, nil
}

// MonitorRects lists the connected monitors' bounds (index order matches the
// CaptureScreen monitor argument).
func MonitorRects() ([]Rect, error) {
	ensureDPIAware()
	return monitorRects()
}
