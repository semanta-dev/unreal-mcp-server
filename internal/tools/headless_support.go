package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
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

// automationResult matches UE's LogAutomationController result line, "Test Completed.
// Result={<state>}" — anchored to that exact form (no fuzzy "...passed" fallback that
// could mis-tally incidental log lines). UE 5.7 writes Success/Fail; older engines
// Passed/Failed.
var automationResult = regexp.MustCompile(`(?i)test completed\. result=\{(\w+)\}`)

// summarizeAutomation pulls the pass/fail tally from the result lines.
func summarizeAutomation(stdout string) map[string]any {
	passed, failed, skipped := 0, 0, 0
	for _, m := range automationResult.FindAllStringSubmatch(stdout, -1) {
		switch strings.ToLower(m[1]) {
		case "passed", "success":
			passed++
		case "failed", "fail":
			failed++
		default: // NotRun, Skipped
			skipped++
		}
	}
	out := map[string]any{"passed": passed, "failed": failed, "ok": failed == 0}
	if skipped > 0 {
		out["skipped"] = skipped
	}
	return out
}
