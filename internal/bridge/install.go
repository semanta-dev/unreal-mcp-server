package bridge

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jdziat/unreal-mcp-server/internal/snippets"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// ensureInstalled makes sure the companion module (matching snippets.Version) is
// resident in the editor. It re-verifies whenever the command channel generation
// changes (reconnect/editor restart), and is otherwise a no-op after the first
// successful install (no per-Call round-trip).
func (b *Bridge) ensureInstalled(ctx context.Context) error {
	gen := b.run.Generation()

	b.mu.Lock()
	if b.installed && b.installedGen == gen {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	cur, err := b.installedVersion(ctx)
	if err != nil {
		return err
	}
	if cur != snippets.Version() {
		if err := b.installModule(ctx); err != nil {
			return err
		}
		// Confirm the sentinel now reads the expected version.
		if got, verr := b.installedVersion(ctx); verr == nil && got != snippets.Version() {
			return fmt.Errorf("%w: after install, editor reports version %d (want %d)", ErrInstall, got, snippets.Version())
		}
		b.logger.Info("installed companion module", "version", snippets.Version(), "was", cur, "mode", b.mode)
		// Best-effort: disable foreground-throttle so backgrounded builds/PIE/
		// screenshots don't stall (README gotcha; UE 5.7 hides the settings type
		// from Python, hence the load_class CDO trick).
		b.applyEditorPerf(ctx)
	}

	b.mu.Lock()
	b.installed = true
	b.installedGen = gen
	b.mu.Unlock()
	return nil
}

func (b *Bridge) markUninstalled() {
	b.mu.Lock()
	b.installed = false
	b.mu.Unlock()
}

// installedVersion reads the editor-side _MCP_BRIDGE_VERSION sentinel (0 if absent).
func (b *Bridge) installedVersion(ctx context.Context) (int, error) {
	s, err := b.Eval(ctx, "globals().get('_MCP_BRIDGE_VERSION', 0)")
	if err != nil {
		return 0, fmt.Errorf("%w: version check: %v", ErrInstall, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, nil // unparseable -> treat as not installed
	}
	return n, nil
}

// applyEditorPerf disables bThrottleCPUWhenNotForeground via the settings CDO
// (best-effort; ignores failure). Runs once per (re)install.
func (b *Bridge) applyEditorPerf(ctx context.Context) {
	const snippet = `import unreal
try:
    _s = unreal.get_default_object(unreal.load_class(None, '/Script/UnrealEd.EditorPerformanceSettings'))
    _s.set_editor_property('bThrottleCPUWhenNotForeground', False)
except Exception:
    pass`
	if _, err := b.run.RunCommand(ctx, snippet, uexec.ModeExecFile); err != nil {
		b.logger.Debug("throttle-off best-effort failed", "err", err)
	}
}

func (b *Bridge) installModule(ctx context.Context) error {
	switch b.mode {
	case ModeOnDisk:
		return b.installOnDisk(ctx)
	default:
		return b.installHotload(ctx)
	}
}

// installHotload loads the embedded module source into __main__. The source is
// wrapped in a base64 bootstrap: the editor's ExecuteFile mode treats a command
// containing a ".py" token as a filename to load (the module's own header
// comment contains "mcp_bridge.py", which triggered "Could not load Python
// file ..."). base64 output can never contain "." , so the bootstrap is
// guaranteed free of that trigger; it decodes and execs the source into globals.
func (b *Bridge) installHotload(ctx context.Context) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(snippets.Source()))
	boot := fmt.Sprintf("import base64\n"+
		"exec(compile(base64.b64decode(%q).decode(\"utf-8\"), \"mcp_bridge\", \"exec\"), globals())", b64)
	res, err := b.run.RunCommand(ctx, boot, uexec.ModeExecFile)
	if err != nil {
		return fmt.Errorf("%w: hotload: %v", ErrInstall, err)
	}
	if !res.Success {
		return fmt.Errorf("%w: hotload:\n%s", ErrInstall, FormatOutput(res))
	}
	return nil
}

// installOnDisk writes the module to <Project>/Intermediate/PyMCP (gitignored,
// no /Content pollution) and imports it into __main__. Go writes the file
// directly (same local machine as the editor); the editor only imports it.
func (b *Bridge) installOnDisk(ctx context.Context) error {
	if b.projDir == "" {
		return fmt.Errorf("%w: ondisk mode requires ProjectDir", ErrInstall)
	}
	dir := filepath.Join(b.projDir, "Intermediate", "PyMCP")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("%w: mkdir %s: %v", ErrInstall, dir, err)
	}
	path := filepath.Join(dir, "mcp_bridge.py")
	if err := os.WriteFile(path, []byte(snippets.Source()), 0o644); err != nil {
		return fmt.Errorf("%w: write %s: %v", ErrInstall, path, err)
	}
	// Import fresh and rebind dispatch + version into __main__ so the sentinel
	// and Call path work identically to hotload.
	pyDir := strings.ReplaceAll(dir, `\`, "/")
	boot := fmt.Sprintf(`import sys, importlib
if %q not in sys.path:
    sys.path.insert(0, %q)
import mcp_bridge as _mcpb
importlib.reload(_mcpb)
globals()['_mcp_dispatch'] = _mcpb._mcp_dispatch
globals()['_mcp_dispatch_native'] = _mcpb._mcp_dispatch_native
globals()['_MCP_BRIDGE_VERSION'] = _mcpb._MCP_BRIDGE_VERSION`, pyDir, pyDir)
	res, err := b.run.RunCommand(ctx, boot, uexec.ModeExecFile)
	if err != nil {
		return fmt.Errorf("%w: ondisk import: %v", ErrInstall, err)
	}
	if !res.Success {
		return fmt.Errorf("%w: ondisk import:\n%s", ErrInstall, FormatOutput(res))
	}
	return nil
}
