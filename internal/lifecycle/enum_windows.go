//go:build windows

package lifecycle

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// EnumerateTokenProcesses returns every running process whose EXE name contains
// exeSubstr AND whose command line carries "<flag>=<token>", as (pid, token) pairs.
// This is the reattach barrier's ground truth (§6): it finds a daemon-owned editor by
// its LIVE command line, so it catches one still in the Launch→record window (before
// its pid was persisted) and is inherently immune to PID recycling (a recycled pid
// won't bear the token). Best-effort: processes it can't open are skipped.
func EnumerateTokenProcesses(exeSubstr, flag string) []TokenProc {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if windows.Process32First(snap, &e) != nil {
		return nil
	}
	var out []TokenProc
	for {
		pid := int(e.ProcessID)
		if pid > 4 && strings.Contains(strings.ToLower(windows.UTF16ToString(e.ExeFile[:])), strings.ToLower(exeSubstr)) {
			if cmd := readCommandLine(pid); cmd != "" {
				if tok := extractToken(cmd, flag); tok != "" {
					out = append(out, TokenProc{PID: pid, Token: tok})
				}
			}
		}
		if windows.Process32Next(snap, &e) != nil {
			break
		}
	}
	return out
}

// ProcessCommandLine returns a process's full command line from its PEB (empty on any
// failure). Used to tell a deliberately-configured sibling server (a custom -command-addr)
// apart from a same-port orphan, so killOrphanSiblings doesn't nuke a concurrent project.
func ProcessCommandLine(pid int) string { return readCommandLine(pid) }

// ProcessToken returns the "<flag>=<token>" value on a single live process's command
// line ("" if absent/unreadable). Used to RE-VERIFY, immediately before a kill, that
// a pid still bears MY token — so a pid recycled since enumeration is never killed.
func ProcessToken(pid int, flag string) string {
	cmd := readCommandLine(pid)
	if cmd == "" {
		return ""
	}
	return extractToken(cmd, flag)
}

// readCommandLine reads a process's command line out of its PEB. x64 layout:
// PEB.ProcessParameters @ 0x20, RTL_USER_PROCESS_PARAMETERS.CommandLine @ 0x70.
func readCommandLine(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	var pbi windows.PROCESS_BASIC_INFORMATION
	var retLen uint32
	if windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation, unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), &retLen) != nil {
		return ""
	}
	// PebBaseAddress is a *PEB in the REMOTE address space; take its numeric value.
	peb := uintptr(unsafe.Pointer(pbi.PebBaseAddress))
	if peb == 0 {
		return ""
	}
	var ppPtr uintptr
	if !readMem(h, peb+0x20, unsafe.Pointer(&ppPtr), unsafe.Sizeof(ppPtr)) || ppPtr == 0 {
		return ""
	}
	// UNICODE_STRING { Length uint16; MaximumLength uint16; _pad uint32; Buffer uintptr }
	var us struct {
		Length, MaximumLength uint16
		_                     uint32
		Buffer                uintptr
	}
	if !readMem(h, ppPtr+0x70, unsafe.Pointer(&us), unsafe.Sizeof(us)) || us.Length == 0 || us.Buffer == 0 {
		return ""
	}
	buf := make([]uint16, us.Length/2)
	if !readMem(h, us.Buffer, unsafe.Pointer(&buf[0]), uintptr(us.Length)) {
		return ""
	}
	return windows.UTF16ToString(buf)
}

func readMem(h windows.Handle, addr uintptr, dst unsafe.Pointer, size uintptr) bool {
	var n uintptr
	err := windows.ReadProcessMemory(h, addr, (*byte)(dst), size, &n)
	return err == nil && n == size
}
