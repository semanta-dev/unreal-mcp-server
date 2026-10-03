package lifecycle

import (
	"path/filepath"
	"runtime"
	"strings"
)

// ProjectKey canonicalizes a project directory (or .uproject path) so two spellings
// of the same project compare equal: absolute, cleaned, symlinks resolved when the
// path exists, the .uproject file reduced to its directory, and case-folded on
// Windows (case-insensitive filesystem).
func ProjectKey(path string) string {
	if path == "" {
		return ""
	}
	p, err := filepath.Abs(path)
	if err != nil {
		p = path
	}
	if strings.EqualFold(filepath.Ext(p), ".uproject") {
		p = filepath.Dir(p)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return filepath.ToSlash(p)
}
