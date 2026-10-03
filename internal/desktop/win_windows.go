//go:build windows

package desktop

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Raw Win32 entry points. golang.org/x/sys/windows wraps a handful of these
// (EnumWindows, GetWindowThreadProcessId, IsWindowVisible, GetForegroundWindow,
// GetCurrentThreadId); the rest are hand-bound here — the same
// dependency-light, raw-syscall approach the repo already uses (see
// cmd/unreal-mcp/siblings_windows.go, internal/lifecycle/enum_windows.go).
var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procGetWindowTextW        = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW  = user32.NewProc("GetWindowTextLengthW")
	procGetWindowRect         = user32.NewProc("GetWindowRect")
	procIsIconic              = user32.NewProc("IsIconic")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procShowWindow            = user32.NewProc("ShowWindow")
	procBringWindowToTop      = user32.NewProc("BringWindowToTop")
	procSetCursorPos          = user32.NewProc("SetCursorPos")
	procGetCursorPos          = user32.NewProc("GetCursorPos")
	procGetSystemMetrics      = user32.NewProc("GetSystemMetrics")
	procSetProcessDpiAwareCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware    = user32.NewProc("SetProcessDPIAware")
	procGetDC                 = user32.NewProc("GetDC")
	procReleaseDC             = user32.NewProc("ReleaseDC")
	procPrintWindow           = user32.NewProc("PrintWindow")
	procSendInput             = user32.NewProc("SendInput")
	procEnumDisplayMonitors   = user32.NewProc("EnumDisplayMonitors")
	procAttachThreadInput     = user32.NewProc("AttachThreadInput")
	procPostMessageW          = user32.NewProc("PostMessageW")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
)

// System-metric indices (winuser.h).
const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

// ShowWindow commands.
const (
	swRestore = 9
	swShow    = 5
)

// cint truncates a Go int to the 32-bit C int the Win32 ABI expects, so
// negative virtual-desktop coordinates (monitors left of / above the primary)
// pass through correctly.
func cint(v int) uintptr { return uintptr(int32(v)) }

// ensureDPIAware makes the process per-monitor-DPI-aware exactly once, so
// capture pixels and cursor coordinates are physical (un-virtualized) on
// high-DPI displays. Falls back to system-DPI awareness on pre-1703 Windows.
var dpiOnce sync.Once

func ensureDPIAware() {
	dpiOnce.Do(func() {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == (HANDLE)-4.
		const perMonitorV2 = ^uintptr(0) - 3
		if r, _, _ := procSetProcessDpiAwareCtx.Call(perMonitorV2); r == 0 {
			procSetProcessDPIAware.Call()
		}
	})
}

func getSystemMetric(i int) int {
	r, _, _ := procGetSystemMetrics.Call(uintptr(i))
	return int(int32(r))
}

func virtualScreenRect() (Rect, error) {
	r := Rect{
		X: getSystemMetric(smXVirtualScreen),
		Y: getSystemMetric(smYVirtualScreen),
		W: getSystemMetric(smCXVirtualScreen),
		H: getSystemMetric(smCYVirtualScreen),
	}
	if r.empty() {
		return Rect{}, fmt.Errorf("virtual screen metrics unavailable")
	}
	return r, nil
}

// monitorRects enumerates monitors via EnumDisplayMonitors; each callback rect
// is the monitor bounds in virtual-desktop coordinates.
var (
	monMu     sync.Mutex
	monResult []Rect
	monCbOnce sync.Once
	monCb     uintptr
)

// lprc is declared unsafe.Pointer (not uintptr) so the deref below is a
// Pointer->Pointer conversion; syscall.NewCallback accepts pointer-sized params,
// and this keeps `go vet` clean (a uintptr->Pointer cast would trip unsafeptr).
func monitorEnumProc(_ uintptr, _ uintptr, lprc unsafe.Pointer, _ uintptr) uintptr {
	rc := (*windows.Rect)(lprc)
	monResult = append(monResult, Rect{
		X: int(rc.Left), Y: int(rc.Top),
		W: int(rc.Right - rc.Left), H: int(rc.Bottom - rc.Top),
	})
	return 1
}

func monitorRects() ([]Rect, error) {
	monMu.Lock()
	defer monMu.Unlock()
	monResult = monResult[:0]
	monCbOnce.Do(func() { monCb = syscall.NewCallback(monitorEnumProc) })
	r, _, err := procEnumDisplayMonitors.Call(0, 0, monCb, 0)
	if r == 0 {
		return nil, fmt.Errorf("EnumDisplayMonitors failed: %v", err)
	}
	out := make([]Rect, len(monResult))
	copy(out, monResult)
	return out, nil
}

// Window enumeration. The EnumWindows callback is created once (syscall.NewCallback
// allocations are process-lifetime and capped, so a fresh callback per call would
// leak); a mutex serializes the shared collector.
var (
	enumMu         sync.Mutex
	enumResult     []Window
	enumForeground windows.HWND
	enumCbOnce     sync.Once
	enumCb         uintptr
)

func enumWindowProc(hwnd uintptr, _ uintptr) uintptr {
	if w, ok := buildWindow(windows.HWND(hwnd), enumForeground); ok {
		enumResult = append(enumResult, w)
	}
	return 1 // continue
}

func enumWindows() ([]Window, error) {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumResult = enumResult[:0]
	enumForeground = windows.GetForegroundWindow()
	enumCbOnce.Do(func() { enumCb = syscall.NewCallback(enumWindowProc) })
	if err := windows.EnumWindows(enumCb, nil); err != nil {
		return nil, err
	}
	out := make([]Window, len(enumResult))
	copy(out, enumResult)
	return out, nil
}

// buildWindow reads a visible, titled top-level window's properties. Returns
// false for invisible or untitled windows (tool tips, message-only windows).
func buildWindow(h windows.HWND, fg windows.HWND) (Window, bool) {
	if !windows.IsWindowVisible(h) {
		return Window{}, false
	}
	tl, _, _ := procGetWindowTextLengthW.Call(uintptr(h))
	if int(tl) == 0 {
		return Window{}, false
	}
	buf := make([]uint16, int(tl)+1)
	n, _, _ := procGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	title := windows.UTF16ToString(buf[:n])
	if title == "" {
		return Window{}, false
	}
	var pid uint32
	windows.GetWindowThreadProcessId(h, &pid)
	var rc windows.Rect
	procGetWindowRect.Call(uintptr(h), uintptr(unsafe.Pointer(&rc)))
	iconic, _, _ := procIsIconic.Call(uintptr(h))
	return Window{
		HWND:       uintptr(h),
		PID:        int(pid),
		Title:      title,
		X:          int(rc.Left),
		Y:          int(rc.Top),
		W:          int(rc.Right - rc.Left),
		H:          int(rc.Bottom - rc.Top),
		Minimized:  iconic != 0,
		Foreground: h == fg,
	}, true
}

func windowByHWND(hwnd uintptr) (Window, bool) {
	return buildWindow(windows.HWND(hwnd), windows.GetForegroundWindow())
}

func windowRect(hwnd uintptr) (Rect, bool) {
	var rc windows.Rect
	r, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rc)))
	if r == 0 {
		return Rect{}, false
	}
	return Rect{X: int(rc.Left), Y: int(rc.Top), W: int(rc.Right - rc.Left), H: int(rc.Bottom - rc.Top)}, true
}

// closeHWND posts WM_CLOSE: for a dialog, the same as its title-bar X (Cancel). It
// does not need the window to be in the foreground.
func closeHWND(hwnd uintptr) error {
	const wmClose = 0x0010
	if r, _, err := procPostMessageW.Call(hwnd, wmClose, 0, 0); r == 0 {
		return fmt.Errorf("PostMessage(WM_CLOSE): %w", err)
	}
	return nil
}

// focusHWND brings a window to the foreground. SetForegroundWindow is subject to
// the OS foreground-lock, so we use the AttachThreadInput trick (attach to the
// current foreground thread's input queue) to reliably steal focus. Best-effort:
// capture proceeds regardless, and the caller re-reads the foreground state.
func focusHWND(hwnd uintptr) error {
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	fg := windows.GetForegroundWindow()
	curTid := windows.GetCurrentThreadId()
	fgTid, _ := windows.GetWindowThreadProcessId(fg, nil)
	if fg != 0 && uint32(fgTid) != curTid {
		procAttachThreadInput.Call(uintptr(curTid), uintptr(fgTid), 1)
		defer procAttachThreadInput.Call(uintptr(curTid), uintptr(fgTid), 0)
	}
	procBringWindowToTop.Call(hwnd)
	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWindow.Call(hwnd)
	return nil
}
