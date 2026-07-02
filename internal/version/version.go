// Package version carries build stamps injected via -ldflags at build time and
// falls back to Go's embedded build info when built with a plain `go build`.
package version

import "runtime/debug"

// These are overwritten by build.ps1 via -ldflags "-X ...". Defaults are used
// for `go run`/`go build` without stamps.
var (
	Version = "dev"
	Commit  = "unknown"
)

// String returns a single-line human-readable build identifier.
func String() string {
	v, c := Version, Commit
	if v == "dev" || c == "unknown" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					if c == "unknown" && s.Value != "" {
						c = s.Value
						if len(c) > 12 {
							c = c[:12]
						}
					}
				}
			}
		}
	}
	return "unreal-mcp " + v + " (" + c + ")"
}
