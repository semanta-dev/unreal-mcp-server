// Command gendocs writes docs/tools.md and docs/migration-v2.md from the tool spec
// table. Run it with `go generate ./internal/tools` (from the repository root);
// TestGeneratedDocsAreCurrent fails when the committed docs are stale.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jdziat/unreal-mcp-server/internal/tools"
)

func main() {
	specs := tools.CatalogForDocs()
	root := filepath.Join("..", "..", "docs") // go generate runs in internal/tools
	for name, body := range map[string]string{
		"tools.md":        tools.ToolsMarkdown(specs),
		"migration-v2.md": tools.MigrationMarkdown(specs),
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
