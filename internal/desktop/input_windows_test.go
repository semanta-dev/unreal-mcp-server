//go:build windows

package desktop

import (
	"testing"
	"unsafe"
)

// sizeof(INPUT) on x64 is 40 bytes. Both concrete union views must match so a
// homogeneous SendInput batch has the correct, uniform cbSize/stride.
func TestInputStructSize(t *testing.T) {
	if got := unsafe.Sizeof(mouseInputUnion{}); got != 40 {
		t.Errorf("sizeof(mouseInputUnion) = %d, want 40", got)
	}
	if got := unsafe.Sizeof(keyInputUnion{}); got != 40 {
		t.Errorf("sizeof(keyInputUnion) = %d, want 40", got)
	}
	if got := unsafe.Sizeof(mouseInput{}); got != 32 {
		t.Errorf("sizeof(mouseInput) = %d, want 32", got)
	}
	if got := unsafe.Sizeof(keybdInput{}); got != 24 {
		t.Errorf("sizeof(keybdInput) = %d, want 24", got)
	}
	// The union member must start at offset 8 (after DWORD type + 4 pad).
	if off := unsafe.Offsetof(mouseInputUnion{}.mi); off != 8 {
		t.Errorf("mouseInputUnion.mi offset = %d, want 8", off)
	}
	if off := unsafe.Offsetof(keyInputUnion{}.ki); off != 8 {
		t.Errorf("keyInputUnion.ki offset = %d, want 8", off)
	}
}
