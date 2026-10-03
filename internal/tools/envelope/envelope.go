// Package envelope is the single result/error contract every tool speaks
// (docs/plans/OVERHAUL_PLAN.md §2.2 R4/R5):
//
//   - success: structuredContent (an object) + one short text summary line;
//   - failure: isError + structuredContent {"error":{code,message,hint,retryable,outcome,details}}
//   - text "[CODE] message — hint".
//
// Codes are a closed set. Errors from the editor (bridge.OpError codes), the protocol
// layer (uexec) and the context are all mapped onto it by Classify.
package envelope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// Code is a machine-branchable error class. The set is closed: Codes lists every member.
type Code string

const (
	InvalidArgument     Code = "INVALID_ARGUMENT"
	NotFound            Code = "NOT_FOUND"
	Conflict            Code = "CONFLICT"
	Precondition        Code = "PRECONDITION"
	PIENotRunning       Code = "PIE_NOT_RUNNING"
	EditorUnreachable   Code = "EDITOR_UNREACHABLE"
	EditorBusy          Code = "EDITOR_BUSY"
	OperationFailed     Code = "OPERATION_FAILED"
	PythonError         Code = "PYTHON_ERROR"
	UnknownOp           Code = "UNKNOWN_OP"
	Unsupported         Code = "UNSUPPORTED"
	UnsupportedPlatform Code = "UNSUPPORTED_PLATFORM"
	Timeout             Code = "TIMEOUT"
	Cancelled           Code = "CANCELLED"
	Internal            Code = "INTERNAL"
)

// Codes is the closed code set.
var Codes = []Code{InvalidArgument, NotFound, Conflict, Precondition, PIENotRunning, EditorUnreachable,
	EditorBusy, OperationFailed, PythonError, UnknownOp, Unsupported, UnsupportedPlatform, Timeout, Cancelled, Internal}

// Outcome reports whether a failed call may still have taken effect in the editor.
const (
	OutcomeNone    = "none"    // nothing happened (or the call was read-only)
	OutcomeUnknown = "unknown" // a mutating call may have run (timeout/cancel/connection loss after send)
)

// Error is the structured tool error.
type Error struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	Hint      string         `json:"hint,omitempty"`
	Retryable bool           `json:"retryable"`
	Outcome   string         `json:"outcome"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("[%s] %s — %s", e.Code, e.Message, e.Hint)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// New builds an Error with outcome "none".
func New(code Code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), Outcome: OutcomeNone}
}

// WithHint sets the hint and returns e.
func (e *Error) WithHint(h string) *Error { e.Hint = h; return e }

// WithDetail adds a detail field and returns e.
func (e *Error) WithDetail(k string, v any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[k] = v
	return e
}

// editorCodes maps the companion module's codes onto the closed set (the original
// code is preserved in details.editor_code).
var editorCodes = map[string]Code{
	"ASSET_NOT_FOUND":      NotFound,
	"CLASS_UNRESOLVED":     NotFound,
	"TARGET_NOT_FOUND":     NotFound,
	"NOT_FOUND":            NotFound,
	"NOT_IN_PIE":           PIENotRunning,
	"BAD_VALUE":            InvalidArgument,
	"PROPERTY_READONLY":    InvalidArgument,
	"PLUGIN_MISSING":       Precondition,
	"NOT_IMPLEMENTED":      Unsupported,
	"SPAWN_FAILED":         OperationFailed,
	"IMPORT_FAILED":        OperationFailed,
	"INPUT_FAILED":         OperationFailed,
	"CAPTURE_START_FAILED": OperationFailed,
	"EDITOR_ERROR":         OperationFailed,
	"UNKNOWN_OP":           UnknownOp,
	"TIMEOUT":              Timeout,
	"EDITOR_BUSY":          EditorBusy,
}

// EditorCode maps a companion-module code onto the closed set (unknown codes →
// OPERATION_FAILED).
func EditorCode(code string) Code {
	if c, ok := editorCodes[code]; ok {
		return c
	}
	return OperationFailed
}

// Classify maps any error onto an envelope Error. mutating reports whether the call
// could have changed editor state, which decides the outcome of a timeout/cancel.
func Classify(err error, mutating bool) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	unknown := OutcomeNone
	if mutating {
		unknown = OutcomeUnknown
	}
	var op *bridge.OpError
	switch {
	case errors.As(err, &op):
		code := EditorCode(op.Code)
		if op.Traceback != "" && (op.Code == "" || op.Code == "EDITOR_ERROR") {
			code = PythonError
		}
		out := &Error{Code: code, Message: op.Message, Retryable: op.Retryable, Outcome: OutcomeNone,
			Details: map[string]any{"op": op.Op}}
		if op.Code != "" {
			out.Details["editor_code"] = op.Code
		}
		if op.Traceback != "" {
			out.Details["traceback"] = op.Traceback
		}
		return out
	case errors.Is(err, uexec.ErrEditorNotFound):
		return &Error{Code: EditorUnreachable, Message: err.Error(), Retryable: true, Outcome: OutcomeNone,
			Hint: "start the editor with the project open (remote execution enabled), or call editor_lifecycle ensure_open"}
	case errors.Is(err, context.Canceled):
		return &Error{Code: Cancelled, Message: "call cancelled", Outcome: unknown, Retryable: !mutating}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, uexec.ErrTimeout):
		return &Error{Code: Timeout, Message: err.Error(), Outcome: unknown, Retryable: !mutating,
			Hint: "the editor may still be running the request; check state before retrying"}
	case errors.Is(err, uexec.ErrConnectionLost):
		return &Error{Code: EditorUnreachable, Message: err.Error(), Outcome: unknown, Retryable: !mutating}
	case errors.Is(err, bridge.ErrInstall):
		return &Error{Code: EditorUnreachable, Message: err.Error(), Outcome: OutcomeNone, Retryable: true,
			Hint: "the companion module could not be installed; check the editor log"}
	}
	return &Error{Code: Internal, Message: err.Error(), Outcome: unknown}
}

// ErrorResult renders e as an isError tool result.
func ErrorResult(e *Error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: e.Error()}},
		StructuredContent: map[string]any{"error": e},
	}
}

// Result renders a successful call: data becomes structuredContent and summary the
// first text line; extra content (images) follows. A nil data yields {}.
func Result(data any, summary string, extra ...mcp.Content) *mcp.CallToolResult {
	if data == nil {
		data = map[string]any{}
	}
	if summary == "" {
		if b, err := json.Marshal(data); err == nil {
			summary = string(b)
		}
	}
	content := append([]mcp.Content{&mcp.TextContent{Text: summary}}, extra...)
	return &mcp.CallToolResult{Content: content, StructuredContent: data}
}

// IsEnveloped reports whether an error result already carries a structured error.
func IsEnveloped(res *mcp.CallToolResult) bool {
	if res == nil || !res.IsError {
		return true
	}
	switch sc := res.StructuredContent.(type) {
	case map[string]any:
		_, ok := sc["error"]
		return ok
	case json.RawMessage:
		return strings.Contains(string(sc), `"error"`)
	}
	return false
}

// DisabledLookup reports the toolset of a tool name that exists but is not enabled
// in this session.
type DisabledLookup func(tool string) (toolset string, ok bool)

// SafetyNet is a receiving middleware for tools/call that guarantees the envelope:
// an isError result without a structured error is rewritten to INTERNAL, and a call
// to a known-but-disabled tool returns a PRECONDITION naming its toolset instead of
// the SDK's protocol-level "unknown tool" error.
func SafetyNet(disabled DisabledLookup) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if method != "tools/call" {
				return res, err
			}
			if err != nil {
				if disabled != nil {
					if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
						if ts, ok := disabled(p.Name); ok {
							return ErrorResult(New(Precondition, "tool %q is in toolset %q, which is not enabled", p.Name, ts).
								WithHint("call toolsets with op=enable, toolset=" + ts)), nil
						}
					}
				}
				return res, err
			}
			if r, ok := res.(*mcp.CallToolResult); ok && !IsEnveloped(r) {
				msg := "tool returned an unstructured error"
				for _, c := range r.Content {
					if t, ok := c.(*mcp.TextContent); ok {
						msg = t.Text
						break
					}
				}
				return ErrorResult(New(Internal, "%s", msg)), nil
			}
			return res, err
		}
	}
}
