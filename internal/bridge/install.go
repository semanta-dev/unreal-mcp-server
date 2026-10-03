package bridge

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ensureInstalled makes sure the companion module (matching CompanionVersion) is
// resident in the editor. It re-verifies whenever the command channel generation
// changes (reconnect/editor restart), and is otherwise a no-op after the first
// successful install (no per-Call round-trip).
func (b *Bridge) ensureInstalled(ctx context.Context) error {
	// Version checks, the (re)install and the perf tweak are all idempotent: safe to
	// re-send if the connection drops after a write.
	ctx = uexec.WithRetryPolicy(ctx, uexec.RetryIdempotent)
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
	if cur != CompanionVersion() {
		if err := b.installModule(ctx); err != nil {
			return err
		}
		// Confirm the sentinel now reads the expected version.
		if got, verr := b.installedVersion(ctx); verr == nil && got != CompanionVersion() {
			return fmt.Errorf("%w: after install, editor reports version %d (want %d)", ErrInstall, got, CompanionVersion())
		}
		b.logger.Info("installed companion module", "version", CompanionVersion(), "was", cur, "mode", b.mode)
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

// installedVersion reads the editor-side _MCP2_BRIDGE_VERSION sentinel (0 if absent).
// A resident module whose source digest differs from this build's reads as -1, so a
// rebuilt server reinstalls changed companion code even when the version number was
// not bumped (an editor outlives server processes).
func (b *Bridge) installedVersion(ctx context.Context) (int, error) {
	s, err := b.Eval(ctx, fmt.Sprintf(
		"globals().get('_MCP2_BRIDGE_VERSION', 0) if globals().get('_MCP2_BRIDGE_DIGEST') == %q else -1", CompanionDigest()))
	if err != nil {
		return 0, fmt.Errorf("%w: version check: %w", ErrInstall, err)
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
	b64 := base64.StdEncoding.EncodeToString([]byte(CompanionSource()))
	boot := fmt.Sprintf("import base64, types\n"+
		"_mcp2 = types.ModuleType(\"mcp2\")\n"+
		"exec(compile(base64.b64decode(%q).decode(\"utf-8\"), \"mcp2_bridge\", \"exec\"), _mcp2.__dict__)\n"+
		"_mcp2_dispatch = _mcp2._mcp2_dispatch\n"+
		"_mcp2_dispatch_native = _mcp2._mcp2_dispatch_native\n"+
		"_MCP2_BRIDGE_VERSION = _mcp2._MCP2_BRIDGE_VERSION\n"+
		"_MCP2_BRIDGE_DIGEST = %q", b64, CompanionDigest())
	res, err := b.run.RunCommand(ctx, boot, uexec.ModeExecFile)
	if err != nil {
		return fmt.Errorf("%w: hotload: %w", ErrInstall, err)
	}
	if !res.Success {
		return fmt.Errorf("%w: hotload:\n%s", ErrInstall, FormatOutput(res))
	}
	return nil
}

// installOnDisk writes the module to <Project>/Intermediate/PyMCP2/mcp2_bridge.py (gitignored,
// no /Content pollution) and imports it into __main__. Go writes the file
// directly (same local machine as the editor); the editor only imports it.
func (b *Bridge) installOnDisk(ctx context.Context) error {
	if b.projDir == "" {
		return fmt.Errorf("%w: ondisk mode requires ProjectDir", ErrInstall)
	}
	// v2 has its own directory and module name: v1 used Intermediate/PyMCP/mcp_bridge.py
	// imported as mcp_bridge, and sharing it would let a handover reload one module
	// object under the other server (plan §2.8).
	dir := filepath.Join(b.projDir, "Intermediate", "PyMCP2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("%w: mkdir %s: %v", ErrInstall, dir, err)
	}
	path := filepath.Join(dir, "mcp2_bridge.py")
	if err := os.WriteFile(path, []byte(CompanionSource()), 0o644); err != nil {
		return fmt.Errorf("%w: write %s: %v", ErrInstall, path, err)
	}
	// Import fresh and rebind dispatch + version into __main__ so the sentinel
	// and Call path work identically to hotload.
	pyDir := strings.ReplaceAll(dir, `\`, "/")
	boot := fmt.Sprintf(`import sys, importlib
if %q not in sys.path:
    sys.path.insert(0, %q)
import mcp2_bridge as _mcpb
importlib.reload(_mcpb)
globals()['_mcp2'] = _mcpb
globals()['_mcp2_dispatch'] = _mcpb._mcp2_dispatch
globals()['_mcp2_dispatch_native'] = _mcpb._mcp2_dispatch_native
globals()['_MCP2_BRIDGE_VERSION'] = _mcpb._MCP2_BRIDGE_VERSION
globals()['_MCP2_BRIDGE_DIGEST'] = %q`, pyDir, pyDir, CompanionDigest())
	res, err := b.run.RunCommand(ctx, boot, uexec.ModeExecFile)
	if err != nil {
		return fmt.Errorf("%w: ondisk import: %w", ErrInstall, err)
	}
	if !res.Success {
		return fmt.Errorf("%w: ondisk import:\n%s", ErrInstall, FormatOutput(res))
	}
	return nil
}

// ClaimNative points the editor's native-dispatch entry point at the v2 companion.
// The MCPCore plugin calls __main__._mcp_dispatch_native by that fixed name
// (MCPCockpitServer.cpp), so this is the one name v2 must share with a v1 companion.
// It is claimed only when v2 attaches the cockpit, and it also invalidates the v1
// install sentinel so that a rollback to v1 reinstalls v1 instead of finding the
// entry point redirected to v2.
func (b *Bridge) ClaimNative(ctx context.Context) error {
	if err := b.ensureInstalled(ctx); err != nil {
		return err
	}
	const claim = "globals()['_mcp_dispatch_native'] = _mcp2._mcp2_dispatch_native\n" +
		"globals()['_MCP_BRIDGE_VERSION'] = 0"
	res, err := b.run.RunCommand(uexec.WithRetryPolicy(ctx, uexec.RetryIdempotent), claim, uexec.ModeExecFile)
	if err != nil {
		return fmt.Errorf("%w: claim native entry point: %w", ErrInstall, err)
	}
	if !res.Success {
		return fmt.Errorf("%w: claim native entry point:\n%s", ErrInstall, FormatOutput(res))
	}
	return nil
}
