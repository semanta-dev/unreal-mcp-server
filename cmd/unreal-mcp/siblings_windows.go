//go:build windows

package main

import (
	"log/slog"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/jdziat/unreal-mcp-server/internal/lifecycle"
)

// killOrphanSiblings terminates any OTHER unreal-mcp.exe process — a stale server left over
// from a previous session. The TCP reverse-connect listener uses SO_REUSEADDR, so on Windows
// two servers can bind the same command port (6776) at once and the editor's reverse-connect
// can land on the dead orphan, silently wedging discovery ("no Unreal Editor node discovered"
// even though the editor is up). Claude Code spawns exactly one server per session, so a
// sibling on the default port is always an orphan — kill it so THIS process owns the port.
// Only called for the default command addr; custom-port concurrent setups are left untouched.
func killOrphanSiblings(logger *slog.Logger) {
	self := os.Getpid()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return
	}
	for {
		if int(e.ProcessID) != self && strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "unreal-mcp.exe") {
			logger.Warn("terminating orphaned sibling unreal-mcp server so this process owns the reverse-connect port", "pid", e.ProcessID)
			_ = lifecycle.Kill(int(e.ProcessID))
		}
		if err := windows.Process32Next(snap, &e); err != nil {
			break
		}
	}
}
