//go:build windows

package desktop

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// INPUT type tags and KEYBDINPUT flags (winuser.h).
const (
	inputMouse    = 0
	inputKeyboard = 1

	keyeventfExtended = 0x0001
	keyeventfKeyUp    = 0x0002
	keyeventfUnicode  = 0x0004
)

// x64 INPUT layout. INPUT is DWORD type + 4-byte pad + a 32-byte union, i.e.
// 40 bytes total. We keep two concrete 40-byte views (mouse, keyboard) and only
// ever send homogeneous batches, so cbSize is uniform.
type mouseInput struct {
	dx, dy      int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type keybdInput struct {
	wVk, wScan  uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type mouseInputUnion struct {
	typ uint32
	_   uint32
	mi  mouseInput // 32 bytes => struct is 40
}

type keyInputUnion struct {
	typ uint32
	_   uint32
	ki  keybdInput // 24 bytes
	_   [8]byte    // pad to 40 to match sizeof(INPUT)
}

func sendInputs(p unsafe.Pointer, n int, size uintptr) error {
	r, _, err := procSendInput.Call(uintptr(n), uintptr(p), size)
	if int(r) != n {
		return fmt.Errorf("SendInput injected %d of %d events: %v", int(r), n, err)
	}
	return nil
}

// sendMouseEvent injects a single mouse event at the current cursor position.
func sendMouseEvent(flags uint32, data int32) error {
	in := mouseInputUnion{typ: inputMouse, mi: mouseInput{mouseData: uint32(data), dwFlags: flags}}
	return sendInputs(unsafe.Pointer(&in), 1, unsafe.Sizeof(in))
}

// moveCursor places the cursor at absolute (physical-pixel) coordinates, which
// may be negative on multi-monitor virtual desktops.
func moveCursor(x, y int) error {
	r, _, err := procSetCursorPos.Call(cint(x), cint(y))
	if r == 0 {
		return fmt.Errorf("SetCursorPos(%d,%d) failed: %v", x, y, err)
	}
	return nil
}

type winPoint struct{ X, Y int32 }

func getCursorPos() (int, int) {
	var p winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return int(p.X), int(p.Y)
}

// sendKeyEvents injects a batch of key transitions in one atomic SendInput call.
func sendKeyEvents(events []KeyEvent) error {
	arr := make([]keyInputUnion, len(events))
	for i, ev := range events {
		flags := uint32(0)
		if ev.Extended {
			flags |= keyeventfExtended
		}
		if ev.Up {
			flags |= keyeventfKeyUp
		}
		arr[i] = keyInputUnion{typ: inputKeyboard, ki: keybdInput{wVk: ev.VK, dwFlags: flags}}
	}
	return sendInputs(unsafe.Pointer(&arr[0]), len(arr), unsafe.Sizeof(arr[0]))
}

// typeUnicode types a string as literal Unicode characters (KEYEVENTF_UNICODE),
// independent of keyboard layout. UTF-16 code units (incl. surrogate pairs) are
// each sent as a down/up pair.
func typeUnicode(s string) error {
	u16 := windows.StringToUTF16(s) // NUL-terminated
	if len(u16) > 0 {
		u16 = u16[:len(u16)-1] // drop the trailing NUL
	}
	if len(u16) == 0 {
		return fmt.Errorf("no characters to type")
	}
	arr := make([]keyInputUnion, 0, len(u16)*2)
	for _, cu := range u16 {
		down := keyInputUnion{typ: inputKeyboard, ki: keybdInput{wScan: cu, dwFlags: keyeventfUnicode}}
		up := keyInputUnion{typ: inputKeyboard, ki: keybdInput{wScan: cu, dwFlags: keyeventfUnicode | keyeventfKeyUp}}
		arr = append(arr, down, up)
	}
	return sendInputs(unsafe.Pointer(&arr[0]), len(arr), unsafe.Sizeof(arr[0]))
}
