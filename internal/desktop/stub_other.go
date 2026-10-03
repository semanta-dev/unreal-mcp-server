//go:build !windows

package desktop

import "image"

// Non-Windows stubs. The MCP server ships for Windows (that is where the Unreal
// Editor and this whole toolchain run); these keep the package compiling on the
// linux CI (go test -race ./...) and make every entry point fail cleanly with
// ErrUnsupported rather than panic. All pure logic (keymap, downscale, window
// selection) is exercised by the cross-platform tests regardless.

func ensureDPIAware() {}

func enumWindows() ([]Window, error) { return nil, ErrUnsupported }

func windowByHWND(uintptr) (Window, bool) { return Window{}, false }

func windowRect(uintptr) (Rect, bool) { return Rect{}, false }

func focusHWND(uintptr) error { return ErrUnsupported }

func virtualScreenRect() (Rect, error) { return Rect{}, ErrUnsupported }

func monitorRects() ([]Rect, error) { return nil, ErrUnsupported }

func captureRegionImg(int, int, int, int) (*image.RGBA, error) { return nil, ErrUnsupported }

func captureWindowImg(uintptr, string) (*image.RGBA, error) { return nil, ErrUnsupported }

func moveCursor(int, int) error { return ErrUnsupported }

func getCursorPos() (int, int) { return 0, 0 }

func sendMouseEvent(uint32, int32) error { return ErrUnsupported }

func sendKeyEvents([]KeyEvent) error { return ErrUnsupported }

func typeUnicode(string) error { return ErrUnsupported }
