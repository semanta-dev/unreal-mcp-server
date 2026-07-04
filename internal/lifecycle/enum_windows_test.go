//go:build windows

package lifecycle

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestReadCommandLineSelf validates the PEB command-line read against this test
// process (whose command line we know is non-empty).
func TestReadCommandLineSelf(t *testing.T) {
	cmd := readCommandLine(os.Getpid())
	if cmd == "" {
		t.Fatal("readCommandLine returned empty for the current process")
	}
}

// TestEnumerateTokenProcessesFindsChild spawns a long-lived cmd.exe carrying a token
// on its command line and confirms EnumerateTokenProcesses locates it by (exe, token)
// — the exact reattach ground-truth path.
func TestEnumerateTokenProcessesFindsChild(t *testing.T) {
	tok := "umcptest-abc123"
	// cmd.exe stays alive ~30s via ping; the token rides on its own command line in
	// a rem (ignored by cmd) so PEB reads it without disturbing the ping.
	child := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul & rem -MCPInstanceToken="+tok)
	if err := child.Start(); err != nil {
		t.Skipf("cannot spawn child: %v", err)
	}
	defer func() { _ = child.Process.Kill() }()

	var found bool
	for i := 0; i < 40 && !found; i++ {
		for _, tp := range EnumerateTokenProcesses("cmd", "-MCPInstanceToken") {
			if tp.Token == tok && tp.PID == child.Process.Pid {
				found = true
			}
		}
		if !found {
			time.Sleep(75 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("EnumerateTokenProcesses did not find the spawned token-bearing process")
	}
}
