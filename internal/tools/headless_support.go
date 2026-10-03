package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

func uprojectPath(projectDir string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(projectDir, "*.uproject"))
	if len(matches) == 0 {
		return "", fmt.Errorf("no .uproject in %s", projectDir)
	}
	return matches[0], nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// summarizeAutomation pulls the pass/fail tally from UE's LogAutomationController
// result lines ("Test Completed. Result={Passed|Failed}"). Anchored to the exact
// form (no fuzzy "...passed" fallback that could mis-tally incidental log lines).
func summarizeAutomation(stdout string) map[string]any {
	passed, failed := 0, 0
	for _, ln := range strings.Split(stdout, "\n") {
		l := strings.ToLower(ln)
		switch {
		case strings.Contains(l, "test completed. result={passed}"):
			passed++
		case strings.Contains(l, "test completed. result={failed}"):
			failed++
		}
	}
	return map[string]any{"passed": passed, "failed": failed, "ok": failed == 0}
}
