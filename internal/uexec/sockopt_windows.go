//go:build windows

package uexec

import "syscall"

// setReuseAddr sets SO_REUSEADDR on a Windows socket handle. Windows has no
// SO_REUSEPORT; SO_REUSEADDR is what the reference client uses to share the
// multicast/command ports with the editor.
func setReuseAddr(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
