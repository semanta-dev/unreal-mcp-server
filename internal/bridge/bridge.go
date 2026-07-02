// Package bridge is the semantic layer over the uexec protocol client. It hot-
// loads the companion Python module, dispatches structured ops into it with
// base64-JSON args (no hand-built Python -> injection-safe), and provides the
// text/JSON/eval helpers that mirror the Python unreal_bridge (GO_REWRITE_PLAN.md §8).
package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

const jsonMarker = "__MCP_JSON__"

// Errors. Op-level failures wrap ErrOp (model-visible); install failures wrap
// ErrInstall (server fault). Protocol issues reuse uexec.ErrProtocol.
var (
	ErrOp      = errors.New("editor op failed")
	ErrInstall = errors.New("companion module install/verify failed")
)

// Runner is the subset of *uexec.Session the bridge depends on (mockable in tests).
type Runner interface {
	RunCommand(ctx context.Context, code string, mode uexec.ExecMode) (uexec.CommandResult, error)
	Generation() uint64
}

// SnippetMode selects how the companion module is delivered to the editor.
type SnippetMode string

const (
	ModeHotload SnippetMode = "hotload" // ExecuteFile the module into __main__ (default; needs __main__ persistence)
	ModeOnDisk  SnippetMode = "ondisk"  // write to <Project>/Intermediate/PyMCP and import (fallback)
)

// Options configures a Bridge.
type Options struct {
	Mode       SnippetMode  // default ModeHotload
	ProjectDir string       // required for ModeOnDisk
	Logger     *slog.Logger // default: discard
}

// Bridge is the semantic client. Safe for the single-flight use the Session enforces.
type Bridge struct {
	run     Runner
	mode    SnippetMode
	projDir string
	logger  *slog.Logger

	mu           sync.Mutex
	installed    bool
	installedGen uint64
}

// New builds a Bridge over a Runner (a *uexec.Session in production).
func New(run Runner, opts Options) *Bridge {
	if opts.Mode == "" {
		opts.Mode = ModeHotload
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Bridge{run: run, mode: opts.Mode, projDir: opts.ProjectDir, logger: opts.Logger}
}

// OpError is an editor-reported op failure (op-level, not transport).
type OpError struct {
	Op        string
	Message   string
	Traceback string
}

func (e *OpError) Error() string {
	if e.Traceback != "" {
		return fmt.Sprintf("op %s failed: %s\n%s", e.Op, e.Message, e.Traceback)
	}
	return fmt.Sprintf("op %s failed: %s", e.Op, e.Message)
}

func (e *OpError) Unwrap() error { return ErrOp }

// dispatchEnvelope is the JSON the companion module emits after the marker.
type dispatchEnvelope struct {
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result"`
	Error     string          `json:"error"`
	Traceback string          `json:"traceback"`
}

// dispatch runs an op in the companion module, ensuring the module is installed
// and reinstalling+retrying once if the editor lost it (restart). It returns the
// parsed envelope AND the underlying command result (so text tools can surface
// captured editor output). It does NOT convert an op failure to an error; the
// caller inspects env.OK.
func (b *Bridge) dispatch(ctx context.Context, op string, args any) (dispatchEnvelope, uexec.CommandResult, error) {
	var env dispatchEnvelope
	var res uexec.CommandResult
	j, err := json.Marshal(args)
	if err != nil {
		return env, res, fmt.Errorf("marshal args for %q: %w", op, err)
	}
	// base64 -> the arg literal is pure ASCII with no chars needing escaping and
	// zero Python-injection surface (the editor does json.loads(base64.b64decode)).
	code := fmt.Sprintf("_mcp_dispatch(%q, %q)", op, base64.StdEncoding.EncodeToString(j))

	for attempt := 0; attempt < 2; attempt++ {
		if err := b.ensureInstalled(ctx); err != nil {
			return env, res, err
		}
		res, err = b.run.RunCommand(ctx, code, uexec.ModeExecFile)
		if err != nil {
			return env, res, err
		}
		raw, ok := extractEnvelope(res)
		if !ok {
			if attempt == 0 && looksUninstalled(res) {
				b.markUninstalled()
				b.logger.Warn("companion op not found; reinstalling", "op", op)
				continue
			}
			return env, res, fmt.Errorf("%w: op %q produced no result payload:\n%s", uexec.ErrProtocol, op, FormatOutput(res))
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return env, res, fmt.Errorf("%w: bad dispatch envelope for %q: %v", uexec.ErrProtocol, op, err)
		}
		return env, res, nil
	}
	return env, res, fmt.Errorf("%w: op %q failed after reinstall", ErrInstall, op)
}

// Call invokes a structured op and returns its result JSON. Op-level failures
// return an *OpError.
func (b *Bridge) Call(ctx context.Context, op string, args any) (json.RawMessage, error) {
	env, _, err := b.dispatch(ctx, op, args)
	if err != nil {
		return nil, err
	}
	if !env.OK {
		return nil, &OpError{Op: op, Message: env.Error, Traceback: env.Traceback}
	}
	return env.Result, nil
}

// CallText invokes a text-style op and returns the op's "message" PLUS any
// captured editor output entries (Info/Warning/Error) EXCEPT the __MCP_JSON__
// envelope line — matching the Python server's format_output, which surfaced
// warnings/errors the editor logged during the op (e.g. save_all failures).
func (b *Bridge) CallText(ctx context.Context, op string, args any) (string, error) {
	env, res, err := b.dispatch(ctx, op, args)
	if err != nil {
		return "", err
	}
	if !env.OK {
		return "", &OpError{Op: op, Message: env.Error, Traceback: env.Traceback}
	}
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(env.Result, &m)
	return joinNonEmpty(m.Message, formatEntriesExcludingMarker(res.Output)), nil
}

// CallInto is Call plus JSON unmarshaling of the result into out.
func (b *Bridge) CallInto(ctx context.Context, op string, args any, out any) error {
	raw, err := b.Call(ctx, op, args)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// formatEntriesExcludingMarker flattens output entries the way format_output
// does, but skips the __MCP_JSON__ envelope line the companion prints.
func formatEntriesExcludingMarker(entries []uexec.OutputEntry) string {
	var lines []string
	for _, e := range entries {
		if strings.Contains(e.Output, jsonMarker) {
			continue
		}
		text := strings.TrimRight(e.Output, "\n")
		if text == "" {
			continue
		}
		if e.Type == "Error" || e.Type == "Warning" {
			lines = append(lines, "["+e.Type+"] "+text)
		} else {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n")
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

// RunPython runs arbitrary code (the escape hatch for execute_python), returning
// the raw command result.
func (b *Bridge) RunPython(ctx context.Context, code string, mode uexec.ExecMode) (uexec.CommandResult, error) {
	return b.run.RunCommand(ctx, code, mode)
}

// Eval evaluates a single expression and returns its repr string; errors if the
// editor reported failure.
func (b *Bridge) Eval(ctx context.Context, expr string) (string, error) {
	res, err := b.run.RunCommand(ctx, expr, uexec.ModeEval)
	if err != nil {
		return "", err
	}
	if !res.Success {
		return "", fmt.Errorf("%w: %s", uexec.ErrCommandFailed, FormatOutput(res))
	}
	return res.Result, nil
}

// RunJSON runs a raw snippet that prints a __MCP_JSON__ payload and returns it
// (parity with the Python unreal_bridge.run_json, for snippets not routed
// through the companion dispatch table).
func (b *Bridge) RunJSON(ctx context.Context, snippet string) (json.RawMessage, error) {
	res, err := b.run.RunCommand(ctx, snippet, uexec.ModeExecFile)
	if err != nil {
		return nil, err
	}
	raw, ok := extractMarker(res)
	if !ok {
		return nil, fmt.Errorf("%w: no %s payload:\n%s", uexec.ErrProtocol, jsonMarker, FormatOutput(res))
	}
	return raw, nil
}

// extractEnvelope finds the first __MCP_JSON__ payload (dispatch envelope).
func extractEnvelope(res uexec.CommandResult) (json.RawMessage, bool) {
	return extractMarker(res)
}

// extractMarker scans command output (then result) for the marker and decodes
// exactly one JSON value after it (robust to trailing content/newlines).
func extractMarker(res uexec.CommandResult) (json.RawMessage, bool) {
	scan := func(s string) (json.RawMessage, bool) {
		idx := strings.Index(s, jsonMarker)
		if idx < 0 {
			return nil, false
		}
		dec := json.NewDecoder(strings.NewReader(s[idx+len(jsonMarker):]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		return raw, true
	}
	for _, e := range res.Output {
		if raw, ok := scan(e.Output); ok {
			return raw, true
		}
	}
	return scan(res.Result)
}

// looksUninstalled reports whether a failed result is a NameError for the
// dispatch function (i.e. the editor restarted and lost the module).
func looksUninstalled(res uexec.CommandResult) bool {
	if res.Success {
		return false
	}
	blob := res.Result + "\n" + FormatOutput(res)
	return strings.Contains(blob, "_mcp_dispatch") &&
		(strings.Contains(blob, "NameError") || strings.Contains(blob, "not defined"))
}

// FormatOutput flattens a command result into readable text (mirrors the Python
// unreal_bridge.format_output exactly).
func FormatOutput(res uexec.CommandResult) string {
	var lines []string
	for _, e := range res.Output {
		text := strings.TrimRight(e.Output, "\n")
		if e.Type == "Error" || e.Type == "Warning" {
			lines = append(lines, fmt.Sprintf("[%s] %s", e.Type, text))
		} else {
			lines = append(lines, text)
		}
	}
	if res.Result != "" && res.Result != "None" {
		lines = append(lines, res.Result)
	}
	if !res.Success {
		lines = append([]string{"[Execution failed]"}, lines...)
	}
	if len(lines) == 0 {
		return "(no output)"
	}
	return strings.Join(lines, "\n")
}
