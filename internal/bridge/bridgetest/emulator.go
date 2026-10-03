// Package bridgetest is an op-level emulator of the editor-side companion module
// (mcp_bridge.py) that plugs into the wire-level fake editor (uexectest.OnCommand).
//
// It understands exactly the commands the real Bridge sends — the version-sentinel
// eval, the hotload/on-disk bootstrap, the editor-perf snippet, the native claim, and
// `_mcp2_dispatch(op, b64)`
// — so an in-process end-to-end test can drive MCP client → tools → bridge → real
// uexec → fake editor with no Unreal process. Ops are scripted per name (OpFunc);
// a small stateful World supplies actor ops so multi-step flows observe real state.
package bridgetest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/jdziat/unreal-mcp-server/internal/uexec/uexectest"
)

const jsonMarker = "__MCP_JSON__"

// OpError is an op-level failure the emulator reports the way the companion's
// _mcp2_dispatch does ({ok:false, error, code, retryable}).
type OpError struct {
	Code      string
	Message   string
	Retryable bool
}

// OpFunc handles one op: it receives the decoded JSON args and returns a JSON-able
// result, or an *OpError.
type OpFunc func(args map[string]any) (any, *OpError)

// Emulator answers fake-editor commands as the companion module would.
type Emulator struct {
	mu           sync.Mutex
	ops          map[string]OpFunc
	python       func(uexectest.CommandRequest) uexectest.CommandResponse
	version      int // installed _MCP2_BRIDGE_VERSION (0 = not installed)
	installs     int
	versionQs    int    // version-sentinel evals received
	nativeClaims int    // ClaimNative commands received
	v1Version    int    // a resident v1 companion's _MCP_BRIDGE_VERSION (0 = none or invalidated)
	installed    string // decoded source of the last successful install
	calls        []string
	pyScripts    []string
}

// New returns an emulator with no ops registered and nothing installed.
func New() *Emulator { return &Emulator{ops: map[string]OpFunc{}} }

// Handle registers (or replaces) an op handler.
func (e *Emulator) Handle(op string, fn OpFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ops[op] = fn
}

// HandlePython sets the handler for raw Python commands (execute_python, console
// snippets) that are not part of the bridge protocol. Default: success echo.
func (e *Emulator) HandlePython(fn func(uexectest.CommandRequest) uexectest.CommandResponse) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.python = fn
}

// Restart simulates an editor restart: the hot-loaded module is gone.
func (e *Emulator) Restart() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.version = 0
}

// Installs reports how many times the companion module was (re)installed.
func (e *Emulator) Installs() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.installs
}

// VersionChecks reports how many version-sentinel evals the bridge sent.
func (e *Emulator) VersionChecks() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.versionQs
}

// InstalledSource returns the decoded module source of the last install ("" if none).
func (e *Emulator) InstalledSource() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.installed
}

// Calls returns the op names dispatched so far, in order.
func (e *Emulator) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

// PythonScripts returns the raw (non-protocol) Python commands received, in order.
func (e *Emulator) PythonScripts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.pyScripts...)
}

// Options returns fakeeditor options wired to this emulator (callers may set
// further fault-injection fields on the result before Start).
func (e *Emulator) Options() uexectest.Options {
	return uexectest.Options{OnCommand: e.OnCommand}
}

var (
	dispatchRe = regexp.MustCompile(`_mcp2_dispatch\("([^"]*)", "([^"]*)"\)`)
	hotloadRe  = regexp.MustCompile(`base64\.b64decode\("([A-Za-z0-9+/=]+)"\)`)
	versionRe  = regexp.MustCompile(`(?m)^_MCP2_BRIDGE_VERSION\s*=\s*(\d+)`)
	ondiskRe   = regexp.MustCompile(`sys\.path\.insert\(0, "([^"]+)"\)`)
)

// OnCommand is the fakeeditor command handler.
func (e *Emulator) OnCommand(req uexectest.CommandRequest) uexectest.CommandResponse {
	code := strings.TrimPrefix(req.Command, "# mcp\n")
	switch {
	case req.ExecMode == "EvaluateStatement" && strings.Contains(code, "_MCP2_BRIDGE_VERSION"):
		e.mu.Lock()
		v := e.version
		e.versionQs++
		e.mu.Unlock()
		return uexectest.CommandResponse{Success: true, Result: strconv.Itoa(v)}
	case strings.Contains(code, "exec(compile(base64.b64decode("):
		return e.install(code)
	case strings.Contains(code, "import mcp_bridge as _mcpb"):
		return e.installOnDisk(code)
	case strings.Contains(code, "_mcp_dispatch_native'] = _mcp2._mcp2_dispatch_native"):
		e.mu.Lock()
		e.nativeClaims++
		if strings.Contains(code, "globals()['_MCP_BRIDGE_VERSION'] = 0") {
			e.v1Version = 0 // a v1 server reconnecting will now reinstall its companion
		}
		e.mu.Unlock()
		return uexectest.CommandResponse{Success: true, Result: "None"}
	case strings.Contains(code, "EditorPerformanceSettings"):
		return uexectest.CommandResponse{Success: true, Result: "None"}
	}
	if m := dispatchRe.FindStringSubmatch(code); m != nil {
		return e.dispatch(m[1], m[2])
	}
	e.mu.Lock()
	e.pyScripts = append(e.pyScripts, code)
	py := e.python
	e.mu.Unlock()
	if py != nil {
		return py(req)
	}
	return uexectest.CommandResponse{Success: true, Result: "None"}
}

// SetV1Resident models a v1 companion already loaded in __main__ (handover tests).
func (e *Emulator) SetV1Resident(version int) {
	e.mu.Lock()
	e.v1Version = version
	e.mu.Unlock()
}

// V1Version is the resident v1 companion's sentinel (0 once invalidated).
func (e *Emulator) V1Version() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.v1Version
}

// NativeClaims reports how many times the server claimed the native entry point.
func (e *Emulator) NativeClaims() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nativeClaims
}

// installOnDisk emulates the on-disk mode: the server wrote mcp_bridge.py into the
// project's Intermediate dir and asks the editor to import it.
func (e *Emulator) installOnDisk(code string) uexectest.CommandResponse {
	m := ondiskRe.FindStringSubmatch(code)
	if m == nil {
		return uexectest.CommandResponse{Success: false, Output: []uexectest.OutputEntry{{Type: "Error", Output: "bridgetest: ondisk boot lacks sys.path"}}}
	}
	raw, err := os.ReadFile(filepath.Join(filepath.FromSlash(m[1]), "mcp_bridge.py"))
	if err != nil {
		return uexectest.CommandResponse{Success: false, Output: []uexectest.OutputEntry{{Type: "Error", Output: "ModuleNotFoundError: " + err.Error()}}}
	}
	return e.installSource(string(raw))
}

// install decodes the hotload payload and installs it only if it is a plausible
// companion module (defines the dispatcher and a version sentinel), so a broken
// embed/concat is caught here rather than silently "installed".
func (e *Emulator) install(code string) uexectest.CommandResponse {
	fail := func(msg string) uexectest.CommandResponse {
		return uexectest.CommandResponse{Success: false, Result: "None",
			Output: []uexectest.OutputEntry{{Type: "Error", Output: msg}}}
	}
	m := hotloadRe.FindStringSubmatch(code)
	if m == nil {
		return fail("bridgetest: hotload payload not found")
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return fail("bridgetest: hotload payload is not base64: " + err.Error())
	}
	return e.installSource(string(raw))
}

// installSource installs a module source if it is a plausible companion.
func (e *Emulator) installSource(src string) uexectest.CommandResponse {
	fail := func(msg string) uexectest.CommandResponse {
		return uexectest.CommandResponse{Success: false, Result: "None",
			Output: []uexectest.OutputEntry{{Type: "Error", Output: msg}}}
	}
	vm := versionRe.FindStringSubmatch(src)
	if vm == nil || !strings.Contains(src, "\ndef _mcp2_dispatch(") {
		return fail("bridgetest: installed source lacks _MCP2_BRIDGE_VERSION or _mcp2_dispatch")
	}
	v, _ := strconv.Atoi(vm[1])
	e.mu.Lock()
	e.version = v
	e.installs++
	e.installed = src
	e.mu.Unlock()
	return uexectest.CommandResponse{Success: true, Result: "None"}
}

func (e *Emulator) dispatch(op, b64 string) uexectest.CommandResponse {
	e.mu.Lock()
	installed := e.version != 0
	fn := e.ops[op]
	if installed {
		e.calls = append(e.calls, op)
	}
	e.mu.Unlock()
	if !installed {
		// What the editor reports when the module was lost (restart): the bridge
		// detects this and reinstalls.
		return uexectest.CommandResponse{Success: false, Result: "None",
			Output: []uexectest.OutputEntry{{Type: "Error", Output: "NameError: name '_mcp_dispatch' is not defined"}}}
	}
	args := map[string]any{}
	if b64 != "" {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || json.Unmarshal(raw, &args) != nil {
			return emit(map[string]any{"ok": false, "error": "bad args", "code": "BAD_VALUE", "retryable": false})
		}
	}
	if fn == nil {
		return emit(map[string]any{"ok": false, "error": "unknown op: " + op, "code": "UNKNOWN_OP", "retryable": false, "traceback": ""})
	}
	res, opErr := fn(args)
	if opErr != nil {
		return emit(map[string]any{"ok": false, "error": opErr.Message, "code": opErr.Code, "retryable": opErr.Retryable})
	}
	return emit(map[string]any{"ok": true, "result": res})
}

func emit(env map[string]any) uexectest.CommandResponse {
	b, err := json.Marshal(env)
	if err != nil {
		panic(fmt.Sprintf("bridgetest: unmarshalable envelope: %v", err))
	}
	return uexectest.CommandResponse{Success: true, Result: "None",
		Output: []uexectest.OutputEntry{{Type: "Info", Output: jsonMarker + string(b) + "\n"}}}
}
