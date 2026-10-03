package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// launcherInstalledPath is where the Epic Games Launcher records engine installs.
// A variable so tests can point it at a fixture.
var launcherInstalledPath = func() string {
	pd := os.Getenv("PROGRAMDATA")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "Epic", "UnrealEngineLauncher", "LauncherInstalled.dat")
}

type launcherInstalled struct {
	InstallationList []struct {
		InstallLocation string `json:"InstallLocation"`
		AppName         string `json:"AppName"` // e.g. "UE_5.7"
	} `json:"InstallationList"`
}

// DiscoverEngine finds an Unreal Engine install when none was configured: the one the
// project's .uproject asks for (EngineAssociation, e.g. "5.7") if installed, else the
// newest installed UE_x.y. It returns "" when nothing is found; engine-dependent tools
// then report a PRECONDITION rather than guessing a path.
func DiscoverEngine(projectDir string) string {
	if v := os.Getenv("UE_ENGINE_DIR"); v != "" {
		return v
	}
	b, err := os.ReadFile(launcherInstalledPath())
	if err != nil {
		return ""
	}
	var li launcherInstalled
	if json.Unmarshal(b, &li) != nil {
		return ""
	}
	type inst struct {
		ver  string
		path string
	}
	var engines []inst
	for _, e := range li.InstallationList {
		if v, ok := strings.CutPrefix(e.AppName, "UE_"); ok && e.InstallLocation != "" {
			engines = append(engines, inst{v, filepath.Join(e.InstallLocation, "Engine")})
		}
	}
	if len(engines) == 0 {
		return ""
	}
	if want := engineAssociation(projectDir); want != "" {
		for _, e := range engines {
			if e.ver == want {
				return e.path
			}
		}
	}
	sort.Slice(engines, func(i, j int) bool { return versionLess(engines[j].ver, engines[i].ver) })
	return engines[0].path
}

// engineAssociation reads EngineAssociation from the project's .uproject.
func engineAssociation(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(projectDir, "*.uproject"))
	if len(matches) == 0 {
		return ""
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return ""
	}
	var up struct {
		EngineAssociation string `json:"EngineAssociation"`
	}
	_ = json.Unmarshal(b, &up)
	return up.EngineAssociation
}

// versionLess compares dotted versions numerically ("5.10" > "5.9").
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, _ := strconv.Atoi(as[i])
		bi, _ := strconv.Atoi(bs[i])
		if ai != bi {
			return ai < bi
		}
	}
	return len(as) < len(bs)
}
