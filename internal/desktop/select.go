package desktop

import (
	"fmt"
	"sort"
	"strings"
)

// editorTitleMarker is the substring every Unreal Editor main window title
// carries, e.g. "AesirWaveDefense - Unreal Editor". Used for auto-detection.
const editorTitleMarker = "unreal editor"

// pickWindow chooses the single best window matching sel. Pure logic (no
// syscalls) so it is unit-tested cross-platform: platform code supplies the
// enumerated slice, this decides the winner.
//
// Precedence: exact HWND, then PID (largest visible window of that process),
// then Title substring, then auto-detect the Unreal Editor. Within a tie,
// prefer a visible, non-minimized, foreground, larger window.
func pickWindow(all []Window, sel Selector) (Window, bool) {
	if sel.HWND != 0 {
		for _, w := range all {
			if w.HWND == sel.HWND {
				return w, true
			}
		}
		return Window{}, false
	}

	var candidates []Window
	switch {
	case sel.PID != 0:
		for _, w := range all {
			if w.PID == sel.PID {
				candidates = append(candidates, w)
			}
		}
	case sel.Title != "":
		needle := strings.ToLower(sel.Title)
		for _, w := range all {
			if strings.Contains(strings.ToLower(w.Title), needle) {
				candidates = append(candidates, w)
			}
		}
	default: // auto: the Unreal Editor
		for _, w := range all {
			if strings.Contains(strings.ToLower(w.Title), editorTitleMarker) {
				candidates = append(candidates, w)
			}
		}
	}
	if len(candidates) == 0 {
		return Window{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return windowRank(candidates[i]) > windowRank(candidates[j])
	})
	return candidates[0], true
}

// windowRank scores a candidate so the "main" window wins: foreground and
// non-minimized beat everything, then larger area (main editor window >> a
// tiny tooltip/child popup).
func windowRank(w Window) int64 {
	var score int64
	if w.Foreground {
		score += 1 << 40
	}
	if !w.Minimized {
		score += 1 << 39
	}
	area := int64(w.W) * int64(w.H)
	if area < 0 {
		area = 0
	}
	return score + area
}

func describeSelector(sel Selector) string {
	switch {
	case sel.HWND != 0:
		return fmt.Sprintf("hwnd=0x%x", sel.HWND)
	case sel.PID != 0:
		return fmt.Sprintf("pid=%d", sel.PID)
	case sel.Title != "":
		return fmt.Sprintf("title~%q", sel.Title)
	default:
		return "auto (Unreal Editor)"
	}
}
