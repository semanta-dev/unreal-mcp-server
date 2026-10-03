package lifecycle

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ProjectFile is a project's optional .umcp.json (per-project server defaults).
type ProjectFile struct {
	Toolsets            []string `json:"toolsets,omitempty"`
	GatePolicy          string   `json:"gate_policy,omitempty"` // "off" | "require"
	KeepPackageRecovery bool     `json:"keep_package_recovery,omitempty"`
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
	return pf, nil
}
