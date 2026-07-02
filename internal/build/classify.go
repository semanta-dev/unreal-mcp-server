package build

import (
	"path/filepath"
	"strings"
)

// Strategy selects how to compile.
type Strategy string

const (
	// StrategyLiveCoding patches function bodies into the running editor. It
	// canNOT add new reflected types/files.
	StrategyLiveCoding Strategy = "livecoding"
	// StrategyFull is a full Build.bat rebuild (editor must be closed).
	StrategyFull Strategy = "full"
)

// FileChange is one entry from `git diff --name-status`.
type FileChange struct {
	Status byte // 'A' added, 'D' deleted, 'M' modified, 'R' renamed, 'C' copied
	Path   string
}

// reflectionMacros trigger UHT codegen; changing them requires a full rebuild
// because Live Coding cannot add/alter reflected types.
var reflectionMacros = []string{
	"UCLASS", "USTRUCT", "UENUM", "UPROPERTY", "UFUNCTION",
	"UINTERFACE", "UDELEGATE", "GENERATED_BODY", "GENERATED_UCLASS_BODY",
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".h", ".hpp", ".inl", ".cpp", ".c", ".cc", ".cs": // .cs = *.Build.cs / *.Target.cs
		return true
	}
	return false
}

// ReflectionTouchedInDiff reports whether any ADDED or REMOVED line in a unified
// diff contains a reflection macro (i.e. a reflected type/member changed).
func ReflectionTouchedInDiff(diff string) bool {
	for _, line := range strings.Split(diff, "\n") {
		if len(line) == 0 {
			continue
		}
		// changed content lines start with a single + or - (not +++/---)
		if (line[0] == '+' && !strings.HasPrefix(line, "+++")) ||
			(line[0] == '-' && !strings.HasPrefix(line, "---")) {
			for _, m := range reflectionMacros {
				if strings.Contains(line, m) {
					return true
				}
			}
		}
	}
	return false
}

// ClassifyStrategy chooses full vs livecoding from the changed files and the
// diff. Any added/deleted/renamed source file, any .Build.cs/.Target.cs change,
// or any reflection-macro change forces a full rebuild; otherwise Live Coding
// can patch the function bodies.
func ClassifyStrategy(changed []FileChange, diff string) (Strategy, string) {
	for _, c := range changed {
		if !isSourceFile(c.Path) {
			continue
		}
		switch c.Status {
		case 'A', 'D', 'R', 'C':
			return StrategyFull, "a source file was added/removed/renamed (Live Coding cannot add translation units)"
		}
		lp := strings.ToLower(c.Path)
		if strings.HasSuffix(lp, ".build.cs") || strings.HasSuffix(lp, ".target.cs") {
			return StrategyFull, "a module build/target rule changed"
		}
	}
	if ReflectionTouchedInDiff(diff) {
		return StrategyFull, "a reflection macro (UCLASS/UPROPERTY/…) changed; UHT must regenerate"
	}
	return StrategyLiveCoding, "only function bodies in existing translation units changed"
}

// ParseNameStatus parses the output of `git diff --name-status`.
func ParseNameStatus(out string) []FileChange {
	var changes []FileChange
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		status := fields[0][0] // R100 -> 'R'
		// For renames/copies, the new path is the last field.
		path := fields[len(fields)-1]
		changes = append(changes, FileChange{Status: status, Path: path})
	}
	return changes
}
