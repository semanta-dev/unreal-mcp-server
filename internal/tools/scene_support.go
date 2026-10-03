package tools

import (
	"errors"
	"os"

	"github.com/jdziat/unreal-mcp-server/internal/scenespec"
)

func loadSpecBytes(specPath, specJSON string) ([]byte, error) {
	if specPath != "" {
		return os.ReadFile(specPath)
	}
	if specJSON != "" {
		return []byte(specJSON), nil
	}
	return nil, errors.New("provide spec_path or spec_json")
}

func diagsToJSON(diags []scenespec.Diagnostic) []map[string]any {
	out := make([]map[string]any, len(diags))
	for i, d := range diags {
		out[i] = map[string]any{"severity": d.Severity, "field": d.Field, "message": d.Message}
	}
	return out
}

func hasErrorDiag(diags []scenespec.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}
