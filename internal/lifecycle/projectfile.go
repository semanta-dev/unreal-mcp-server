package lifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ProjectFile is a project's optional .umcp.json (per-project server defaults).
type ProjectFile struct {
	Toolsets            []string        `json:"toolsets,omitempty"`
	GatePolicy          string          `json:"gate_policy,omitempty"` // "off" | "require"
	KeepPackageRecovery bool            `json:"keep_package_recovery,omitempty"`
	GameAPIRaw          json.RawMessage `json:"game_api,omitempty"`

	// GameAPI is the validated game_api declaration (nil when absent or invalid);
	// GameAPIErr says why an invalid one was rejected. An invalid game_api disables only
	// the game toolset: the rest of the file (gate_policy above all) still applies.
	GameAPI    *GameAPI `json:"-"`
	GameAPIErr string   `json:"-"`
}

// GameAPI declares the game's own agent interface (REMEDIATION_PLAN.md R1.4,
// docs/plans/GAME_CONTRACT.md): a World or GameInstance subsystem of one of the
// project's own modules and the UFUNCTIONs the game tools call on it.
type GameAPI struct {
	Version      int    `json:"version"`
	Object       string `json:"object"` // "@subsystem:<Module>.<Class>" (or /Script/<Module>.<Class>)
	Capabilities string `json:"capabilities"`
	Snapshot     string `json:"snapshot"`
	Command      string `json:"command"`
	Events       string `json:"events"`
	// Module and Class are parsed from Object.
	Module string `json:"-"`
	Class  string `json:"-"`
}

var (
	gameAPIObject = regexp.MustCompile(`^@subsystem:(?:/Script/)?([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)$`)
	ufunctionName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ClassPath is the subsystem's class path (/Script/<Module>.<Class>).
func (g *GameAPI) ClassPath() string { return "/Script/" + g.Module + "." + g.Class }

// parseGameAPI validates a game_api declaration strictly: version 1, no unknown
// fields, every function named, and the object a subsystem class of one of the
// project's own modules (from its .uproject) — never an engine, editor or plugin class.
func parseGameAPI(raw json.RawMessage, modules []string) (*GameAPI, error) {
	var g GameAPI
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&g); err != nil {
		return nil, fmt.Errorf("game_api: %v", err)
	}
	if g.Version != 1 {
		return nil, fmt.Errorf("game_api: version must be 1 (got %d)", g.Version)
	}
	m := gameAPIObject.FindStringSubmatch(g.Object)
	if m == nil {
		return nil, fmt.Errorf("game_api: object must be \"@subsystem:<Module>.<Class>\" (got %q)", g.Object)
	}
	g.Module, g.Class = m[1], m[2]
	own := false
	for _, mod := range modules {
		own = own || strings.EqualFold(mod, g.Module)
	}
	if !own {
		return nil, fmt.Errorf("game_api: %s is not one of the project's own modules %v (engine, editor and plugin classes are not allowed)", g.Module, modules)
	}
	for name, fn := range map[string]string{"capabilities": g.Capabilities, "snapshot": g.Snapshot, "command": g.Command, "events": g.Events} {
		if !ufunctionName.MatchString(fn) {
			return nil, fmt.Errorf("game_api: %s must name a UFUNCTION (got %q)", name, fn)
		}
	}
	return &g, nil
}

// projectModules lists the module names a project's .uproject declares.
func projectModules(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.uproject"))
	var mods []string
	for _, up := range matches {
		b, err := os.ReadFile(up)
		if err != nil {
			continue
		}
		var u struct {
			Modules []struct {
				Name string `json:"Name"`
			} `json:"Modules"`
		}
		if json.Unmarshal(b, &u) == nil {
			for _, m := range u.Modules {
				mods = append(mods, m.Name)
			}
		}
	}
	return mods
}

// ProjectFileName is the per-project config file name.
const ProjectFileName = ".umcp.json"

// LoadProjectFile reads <dir>/.umcp.json. A missing file is not an error (zero value).
func LoadProjectFile(dir string) (ProjectFile, error) {
	var pf ProjectFile
	if dir == "" {
		return pf, nil
	}
	if strings.EqualFold(filepath.Ext(dir), ".uproject") {
		dir = filepath.Dir(dir)
	}
	b, err := os.ReadFile(filepath.Join(dir, ProjectFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return pf, nil
	}
	if err != nil {
		return pf, err
	}
	if err := json.Unmarshal(b, &pf); err != nil {
		return pf, err
	}
	switch pf.GatePolicy {
	case "", "off", "require":
	default:
		return pf, errors.New(ProjectFileName + `: gate_policy must be "off" or "require"`)
	}
	if len(pf.GameAPIRaw) > 0 {
		g, gerr := parseGameAPI(pf.GameAPIRaw, projectModules(dir))
		if gerr != nil {
			pf.GameAPIErr = gerr.Error()
		} else {
			pf.GameAPI = g
		}
	}
	return pf, nil
}
