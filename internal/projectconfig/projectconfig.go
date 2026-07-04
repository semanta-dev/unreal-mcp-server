// Package projectconfig edits a UE project's Config/*.ini files idempotently and
// OFFLINE (pure Go, no editor). It backs the highest-ROI authoring the plan calls
// out: the project default GameMode (DefaultEngine.ini), legacy Enhanced-Input-
// free input mappings (DefaultInput.ini — the path aesir actually uses), and
// gameplay tags (DefaultGameplayTags.ini). Re-applying the same setting is a
// no-op, and updating an existing binding replaces it rather than duplicating.
package projectconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ini is a line-oriented view of one .ini file (UE ini isn't standard INI —
// duplicate "+Array=" keys are meaningful — so we operate on lines, not a map).
type ini struct {
	path  string
	lines []string
}

func load(path string) (*ini, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ini{path: path}, nil
		}
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return &ini{path: path, lines: strings.Split(text, "\n")}, nil
}

func (f *ini) save() error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	out := strings.Join(f.lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(f.path, []byte(out), 0o644)
}

// sectionBounds returns [start,end) line indices of a section's body (excluding
// the header line), and whether it exists. end is the index of the next section
// header or len(lines).
func (f *ini) sectionBounds(section string) (hdr, start, end int, found bool) {
	header := "[" + section + "]"
	hdr = -1
	for i, ln := range f.lines {
		t := strings.TrimSpace(ln)
		if t == header {
			hdr = i
			start = i + 1
			found = true
			continue
		}
		if found && strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			return hdr, start, i, true
		}
	}
	if found {
		return hdr, start, len(f.lines), true
	}
	return -1, 0, 0, false
}

// ensureSection appends an empty section if missing and returns its body bounds.
func (f *ini) ensureSection(section string) (start, end int) {
	if _, s, e, ok := f.sectionBounds(section); ok {
		return s, e
	}
	if len(f.lines) > 0 && strings.TrimSpace(f.lines[len(f.lines)-1]) != "" {
		f.lines = append(f.lines, "")
	}
	f.lines = append(f.lines, "["+section+"]")
	return len(f.lines), len(f.lines)
}

// setSingle sets Key=Value in a section, replacing an existing Key= line or
// appending. Idempotent (identical value => no change reported, but always safe).
func (f *ini) setSingle(section, key, value string) {
	start, end := f.ensureSection(section)
	line := key + "=" + value
	for i := start; i < end && i < len(f.lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(f.lines[i]), key+"=") {
			f.lines[i] = line
			return
		}
	}
	f.insertAt(end, line)
}

// addArray adds "+key=value" under a section. An existing "+key=" line that
// contains ALL of matchAll is treated as the same identity and REPLACED (so
// re-binding updates in place); an exact-equal line is a no-op; anything else is
// appended. matchAll lets an action match by name (one key per action) while an
// axis matches by name+key (multiple keys per axis coexist).
func (f *ini) addArray(section, key, value string, matchAll []string) {
	start, end := f.ensureSection(section)
	line := "+" + key + "=" + value
	prefix := "+" + key + "="
	for i := start; i < end && i < len(f.lines); i++ {
		t := strings.TrimSpace(f.lines[i])
		if !strings.HasPrefix(t, prefix) {
			continue
		}
		if t == line {
			return // exact dup -> no-op
		}
		if len(matchAll) > 0 && containsAll(t, matchAll) {
			f.lines[i] = line // same identity, different binding -> replace
			return
		}
	}
	f.insertAt(end, line)
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func (f *ini) insertAt(idx int, line string) {
	if idx > len(f.lines) {
		idx = len(f.lines)
	}
	f.lines = append(f.lines[:idx], append([]string{line}, f.lines[idx:]...)...)
}

func configPath(configDir, file string) string { return filepath.Join(configDir, file) }

// --- public, task-shaped editors -------------------------------------------

// SetGameMode sets the project's global default GameMode class in DefaultEngine.ini.
// gmClassPath is a class path like /Game/BP/BP_GameMode.BP_GameMode_C or
// /Script/Module.MyGameMode.
func SetGameMode(configDir, gmClassPath string) error {
	f, err := load(configPath(configDir, "DefaultEngine.ini"))
	if err != nil {
		return err
	}
	f.setSingle("/Script/EngineSettings.GameMapsSettings", "GlobalDefaultGameMode", gmClassPath)
	return f.save()
}

// ActionMapping is a legacy (non-Enhanced) input action binding.
type ActionMapping struct {
	Name                  string
	Key                   string // UE key name, e.g. SpaceBar, LeftMouseButton, W
	Shift, Ctrl, Alt, Cmd bool
}

// AddActionMapping adds/updates an action mapping in DefaultInput.ini.
func AddActionMapping(configDir string, m ActionMapping) error {
	f, err := load(configPath(configDir, "DefaultInput.ini"))
	if err != nil {
		return err
	}
	val := fmt.Sprintf(`(ActionName="%s",bShift=%s,bCtrl=%s,bAlt=%s,bCmd=%s,Key=%s)`,
		m.Name, b(m.Shift), b(m.Ctrl), b(m.Alt), b(m.Cmd), m.Key)
	// One key per action: rebind by name (replaces any existing binding for it).
	f.addArray("/Script/Engine.InputSettings", "ActionMappings", val,
		[]string{fmt.Sprintf(`ActionName="%s"`, m.Name)})
	return f.save()
}

// AxisMapping is a legacy input axis binding.
type AxisMapping struct {
	Name  string
	Key   string
	Scale float64
}

// AddAxisMapping adds/updates an axis mapping in DefaultInput.ini.
func AddAxisMapping(configDir string, m AxisMapping) error {
	f, err := load(configPath(configDir, "DefaultInput.ini"))
	if err != nil {
		return err
	}
	val := fmt.Sprintf(`(AxisName="%s",Scale=%s,Key=%s)`, m.Name, trimFloat(m.Scale), m.Key)
	// Multiple keys per axis coexist; the same axis+key updates its scale in place.
	f.addArray("/Script/Engine.InputSettings", "AxisMappings", val,
		[]string{fmt.Sprintf(`AxisName="%s"`, m.Name), "Key=" + m.Key})
	return f.save()
}

// AddGameplayTag appends a gameplay tag to DefaultGameplayTags.ini (idempotent).
func AddGameplayTag(configDir, tag, comment string) error {
	f, err := load(configPath(configDir, "DefaultGameplayTags.ini"))
	if err != nil {
		return err
	}
	val := fmt.Sprintf(`(Tag="%s",DevComment="%s")`, tag, comment)
	f.addArray("/Script/GameplayTags.GameplayTagsSettings", "GameplayTagList", val, []string{fmt.Sprintf(`Tag="%s"`, tag)})
	return f.save()
}

func b(v bool) string {
	if v {
		return "True"
	}
	return "False"
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.3f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
