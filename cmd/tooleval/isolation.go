package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Run isolation beyond git_revert. The revert restores tracked files, but what an agent
// creates is untracked — Widget Blueprints, C++ sources, config files, scenarios — and
// the editor keeps such assets loaded, so a later run would find (or be scored on) an
// earlier run's work; C++ an agent compiled stays in the game module until a rebuild.
// The harness lists the untracked files once at the start (the baseline) and, at each
// reset, deletes any file not on it, restarts the editor, and rebuilds when sources
// changed.

// buildOutputDirs never count: the engine writes them on every run.
var buildOutputDirs = []string{"Saved/", "Intermediate/", "Binaries/", "DerivedDataCache/", ".vs/"}

// untrackedFiles lists a project's untracked files (slash paths, relative), build output
// excluded.
func untrackedFiles(project string) (map[string]bool, error) {
	cmd := exec.Command("git", "-C", project, "status", "--porcelain", "--untracked-files=all", "-z")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status in %s: %w", project, err)
	}
	out := map[string]bool{}
	for _, rec := range strings.Split(string(raw), "\x00") {
		if !strings.HasPrefix(rec, "?? ") {
			continue
		}
		p := strings.TrimPrefix(rec, "?? ")
		skip := false
		for _, d := range buildOutputDirs {
			if strings.HasPrefix(p, d) || strings.Contains(p, "/"+d) {
				skip = true
				break
			}
		}
		if !skip {
			out[p] = true
		}
	}
	return out, nil
}

// newUntracked is what appeared since the baseline listing.
func newUntracked(project string, baseline map[string]bool) ([]string, error) {
	now, err := untrackedFiles(project)
	if err != nil {
		return nil, err
	}
	var extra []string
	for p := range now {
		if !baseline[p] {
			extra = append(extra, p)
		}
	}
	return extra, nil
}

func touchesSource(paths []string) bool {
	for _, p := range paths {
		if strings.HasPrefix(p, "Source/") || strings.Contains(p, "/Source/") || strings.HasSuffix(p, ".Build.cs") {
			return true
		}
	}
	return false
}

// isolate runs after git_revert: deletes the new untracked files (the editor closed
// first — it holds loaded assets open), relaunches it, and rebuilds when C++ changed.
func isolate(ctx context.Context, s *liveSession, project string, baseline map[string]bool, rebuild bool) error {
	extra, err := newUntracked(project, baseline)
	if err != nil {
		return err
	}
	rebuild = rebuild || touchesSource(extra)
	if len(extra) == 0 && !rebuild {
		return nil
	}
	if len(extra) > 0 {
		// Close the editor gracefully (nothing is saved: the revert discarded dirty
		// packages, and quit does not prompt for the deleted files' packages).
		_, _ = s.call(ctx, "python", map[string]any{"op": "run", "code": "unreal.SystemLibrary.quit_editor()"})
		deadline := time.Now().Add(2 * time.Minute)
		for {
			if _, err := s.call(ctx, "editor", map[string]any{"op": "ping"}); err != nil {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("the editor did not quit to clear %d new file(s)", len(extra))
			}
			time.Sleep(2 * time.Second)
		}
		for _, p := range extra {
			full := filepath.Join(project, filepath.FromSlash(p))
			var rerr error
			for i := 0; i < 10; i++ { // the process may still be releasing handles
				if rerr = os.Remove(full); rerr == nil || os.IsNotExist(rerr) {
					rerr = nil
					break
				}
				time.Sleep(time.Second)
			}
			if rerr != nil {
				return fmt.Errorf("remove %s: %w", p, rerr)
			}
		}
	}
	out, err := s.call(ctx, "editor_lifecycle", map[string]any{"op": "ensure_open", "wait_s": 25})
	if err == nil {
		_, err = s.waitJob(ctx, out, 10*time.Minute)
	}
	if err != nil {
		return err
	}
	if rebuild {
		out, err := s.call(ctx, "build", map[string]any{"strategy": "ubt", "wait_s": 25})
		if err == nil {
			_, err = s.waitJob(ctx, out, 20*time.Minute)
		}
		if err != nil {
			return fmt.Errorf("rebuild after the revert: %w", err)
		}
	}
	return nil
}
