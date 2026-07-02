//go:build !windows

package uexec

import "syscall"

// setReuseAddr sets SO_REUSEADDR on a POSIX socket fd. This path exists so the
// non-multicast protocol logic builds and tests on Linux CI; production is Windows.
func setReuseAddr(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
