// Package lifecycle manages the Unreal Editor process: resolving the editor
// executable, launching the project, and checking process liveness. Used by
// AUTO_RELAUNCH crash recovery (P6) and editor_restart / full C++ rebuilds (P7),
// which require the editor to be closed then relaunched (GO_REWRITE_PLAN.md §9 Group I).
package lifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EditorExe returns the UnrealEditor.exe path for an engine directory.
func EditorExe(engineDir string) string {
	return filepath.Join(engineDir, "Engine", "Binaries", "Win64", "UnrealEditor.exe")
}

// FindUproject returns the first .uproject directly under dir, or "".
func FindUproject(dir string) string {
	if dir == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.uproject"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// EditorTarget derives the editor build target from a .uproject path
// (AesirWaveDefense.uproject -> AesirWaveDefenseEditor).
func EditorTarget(uproject string) string {
	base := filepath.Base(uproject)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return name + "Editor"
}

// Launch starts the editor with the given .uproject and extra args. It is NOT
// tied to ctx (the editor must outlive the launching operation); ctx only bounds
// the spawn itself. Returns the started process's PID.
func Launch(engineDir, uproject string, extraArgs ...string) (int, error) {
	exe := EditorExe(engineDir)
	if _, err := os.Stat(exe); err != nil {
		return 0, fmt.Errorf("editor executable not found at %s: %w", exe, err)
	}
	if _, err := os.Stat(uproject); err != nil {
		return 0, fmt.Errorf("uproject not found at %s: %w", uproject, err)
	}
	args := append([]string{uproject}, extraArgs...)
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("launch editor: %w", err)
	}
	// Reap the child in the background so it doesn't become a zombie; we track it
	// by PID via discovery, not by this handle.
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// Kill force-terminates a process by PID (best-effort, cross-platform via os.Process.Kill:
// TerminateProcess on Windows, SIGKILL elsewhere). Used to close an editor that ignored the
// graceful `quit` console command — which only closes PIE, not the editor when no PIE is
// running — so a full rebuild is never blocked by a still-running editor holding the module
// DLL or the Live Coding lock. Returns nil for pid<=0 or an already-gone process.
func Kill(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
