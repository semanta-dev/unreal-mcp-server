package lifecycle

import "strings"

// TokenProc is a running process bearing a launch-flag token (e.g. an editor started
// with -MCPInstanceToken=<token>), as found by EnumerateTokenProcesses.
type TokenProc struct {
	PID   int
	Token string
}

// extractToken pulls the value of "<flag>=<token>" out of a command line, ending the
// token at whitespace or a quote. Returns "" if the flag is absent. Pure + testable.
func extractToken(cmdline, flag string) string {
	prefix := flag + "="
	i := indexOf(cmdline, prefix)
	if i < 0 {
		return ""
	}
	rest := cmdline[i+len(prefix):]
	for j := 0; j < len(rest); j++ {
		switch rest[j] {
		case ' ', '\t', '"', '\r', '\n':
			return rest[:j]
		}
	}
	return rest
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// namesProject reports whether an editor command line opens uproject (paths compared
// case-insensitively with slashes normalized). Pure + testable.
func namesProject(cmdline, uproject string) bool {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, `\`, "/")) }
	return uproject != "" && strings.Contains(norm(cmdline), norm(uproject))
}
