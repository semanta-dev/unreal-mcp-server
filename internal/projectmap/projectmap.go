// Package projectmap builds a static map of a UE project's C++ surface WITHOUT a
// running editor: it parses the .uproject module list, each module's *.Build.cs
// dependencies, and scans headers for UCLASS/USTRUCT/UENUM/UINTERFACE reflected
// types, resolving each to its /Script path. This is the cold-start orientation
// an autonomous agent needs to stop guessing /Script/... paths (and the only
// project map a headless/offline server can produce).
package projectmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Module is one game/plugin module from the .uproject (or a discovered Build.cs).
type Module struct {
	Name         string   `json:"name"`
	Type         string   `json:"type,omitempty"` // Runtime|Editor|...
	Dependencies []string `json:"dependencies,omitempty"`
}

// TypeEntry is one reflected C++ type and where it lives.
type TypeEntry struct {
	Name       string `json:"name"`        // C++ name, e.g. AEnemyCharacter
	ScriptPath string `json:"script_path"` // /Script/<Module>.<reflected-name>
	Kind       string `json:"kind"`        // class|struct|enum|interface
	Module     string `json:"module"`
	Header     string `json:"header"`           // path relative to projectDir
	Parent     string `json:"parent,omitempty"` // C++ parent name, when parseable
}

// Map is the whole project surface.
type Map struct {
	Project string      `json:"project"`
	Modules []Module    `json:"modules"`
	Types   []TypeEntry `json:"types"`
}

var (
	reModulesBlock = regexp.MustCompile(`(?s)"Modules"\s*:\s*\[(.*?)\]`)
	reDepBlock     = regexp.MustCompile(`(?s)(?:Public|Private)DependencyModuleNames\s*\.\s*Add(?:Range)?\s*\(([^;]*?)\)\s*;`)
	reQuoted       = regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_]*)"`)
	reMacro        = regexp.MustCompile(`^\s*(UCLASS|USTRUCT|UENUM|UINTERFACE)\s*\(`)
	reClassDecl    = regexp.MustCompile(`^\s*(?:class|struct)\s+(?:[A-Z][A-Z0-9_]*_API\s+)?([A-Za-z_]\w*)\s*(?::\s*public\s+([A-Za-z_]\w*))?`)
	reEnumDecl     = regexp.MustCompile(`^\s*enum(?:\s+class)?\s+([A-Za-z_]\w*)`)
)

// Scan builds the Map for a project directory (the folder holding the .uproject).
func Scan(projectDir string) (*Map, error) {
	m := &Map{}
	uproj, err := findUProject(projectDir)
	if err != nil {
		return nil, err
	}
	m.Project = filepath.Base(uproj)
	if data, err := os.ReadFile(uproj); err == nil {
		m.Modules = ParseUProject(data)
	}

	// Index Build.cs deps by module name, and scan headers under Source/.
	depByModule := map[string][]string{}
	source := filepath.Join(projectDir, "Source")
	_ = filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		switch {
		case strings.HasSuffix(name, ".Build.cs"):
			mod := strings.TrimSuffix(name, ".Build.cs")
			if content, err := os.ReadFile(path); err == nil {
				depByModule[mod] = ParseBuildCs(string(content))
			}
		case strings.HasSuffix(name, ".h"):
			mod := moduleOf(source, path)
			rel, _ := filepath.Rel(projectDir, path)
			if content, err := os.ReadFile(path); err == nil {
				m.Types = append(m.Types, ScanHeader(string(content), mod, filepath.ToSlash(rel))...)
			}
		}
		return nil
	})

	// Ensure a module entry exists for every module we found a Build.cs for, and
	// attach dependencies.
	have := map[string]int{}
	for i, mod := range m.Modules {
		have[mod.Name] = i
	}
	for mod, deps := range depByModule {
		if i, ok := have[mod]; ok {
			m.Modules[i].Dependencies = deps
		} else {
			m.Modules = append(m.Modules, Module{Name: mod, Dependencies: deps})
			have[mod] = len(m.Modules) - 1
		}
	}
	return m, nil
}

// ParseUProject extracts the module list from a .uproject's JSON.
func ParseUProject(data []byte) []Module {
	var proj struct {
		Modules []Module `json:"Modules"`
	}
	if err := json.Unmarshal(data, &proj); err == nil {
		return proj.Modules
	}
	// Fall back to a lenient regex if the JSON has trailing commas etc.
	var out []Module
	if b := reModulesBlock.FindSubmatch(data); b != nil {
		for _, q := range reQuoted.FindAllStringSubmatch(string(b[1]), -1) {
			out = append(out, Module{Name: q[1]})
		}
	}
	return out
}

// ParseBuildCs returns the distinct dependency module names in a *.Build.cs.
func ParseBuildCs(content string) []string {
	seen := map[string]bool{}
	var out []string
	for _, block := range reDepBlock.FindAllStringSubmatch(content, -1) {
		for _, q := range reQuoted.FindAllStringSubmatch(block[1], -1) {
			if !seen[q[1]] {
				seen[q[1]] = true
				out = append(out, q[1])
			}
		}
	}
	return out
}

// ScanHeader finds the reflected types declared in one header's contents. A
// UCLASS/USTRUCT/UENUM/UINTERFACE macro line is matched to the class/struct/enum
// declaration on a following line.
func ScanHeader(content, module, headerRel string) []TypeEntry {
	lines := strings.Split(content, "\n")
	var out []TypeEntry
	for i, line := range lines {
		mm := reMacro.FindStringSubmatch(line)
		if mm == nil {
			continue
		}
		macro := mm[1]
		// Find the declaration within the next few lines.
		for j := i + 1; j < len(lines) && j < i+4; j++ {
			decl := lines[j]
			if macro == "UENUM" {
				if em := reEnumDecl.FindStringSubmatch(decl); em != nil {
					out = append(out, entry(em[1], "", "enum", module, headerRel))
					break
				}
				continue
			}
			if cm := reClassDecl.FindStringSubmatch(decl); cm != nil {
				kind := "class"
				if macro == "USTRUCT" {
					kind = "struct"
				} else if macro == "UINTERFACE" {
					kind = "interface"
				}
				out = append(out, entry(cm[1], cm[2], kind, module, headerRel))
				break
			}
		}
	}
	return out
}

// entry builds a TypeEntry, resolving the /Script path.
func entry(name, parent, kind, module, header string) TypeEntry {
	return TypeEntry{
		Name:       name,
		ScriptPath: "/Script/" + module + "." + reflectedName(name),
		Kind:       kind,
		Module:     module,
		Header:     header,
		Parent:     parent,
	}
}

// reflectedName drops UE's C++ type prefix to get the reflected name used in a
// /Script path (AEnemyCharacter -> EnemyCharacter). It strips A/U (UObject
// classes), F (structs), and I/U (interfaces) — but NOT the E on enums: UE keeps
// enum prefixes, so EWaveState stays EWaveState (/Script/Module.EWaveState).
func reflectedName(cpp string) string {
	if len(cpp) >= 2 && strings.ContainsRune("AUFI", rune(cpp[0])) && cpp[1] >= 'A' && cpp[1] <= 'Z' {
		return cpp[1:]
	}
	return cpp
}

// moduleOf infers the module a header belongs to from its first path segment
// under Source/.
func moduleOf(sourceDir, headerPath string) string {
	rel, err := filepath.Rel(sourceDir, headerPath)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func findUProject(projectDir string) (string, error) {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".uproject") {
			return filepath.Join(projectDir, e.Name()), nil
		}
	}
	return "", os.ErrNotExist
}
