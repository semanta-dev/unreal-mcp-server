// Package cockpit is the Go side of the MCP Cockpit editor-plugin transport
// (EDITOR_PLUGIN_PLAN.md §2.4/§2.5). The Go server forward-dials the editor's
// ephemeral loopback port and speaks ONE ordered stream of length-prefixed JSON
// frames — events + rpc + progress + control — over a single persistent socket,
// replacing the reverse-connect + marker-scraping uexec path with framed push.
//
// This file is the wire protocol: the frame union + length-prefixed codec. It has no
// I/O policy beyond framing, so it is exhaustively unit-testable without a socket.
package cockpit

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

// ProtocolVersion is bumped on any incompatible frame change; the handshake rejects
// a mismatch loudly rather than mis-parsing.
const ProtocolVersion = 1

// MaxFrameBytes caps a single frame so a corrupt/hostile length can't OOM the reader.
const MaxFrameBytes = 64 << 20 // 64 MiB (a large asset walk result fits; nothing legit exceeds it)

// FrameType discriminates the union. One "type" field per frame keeps the Go<->C++
// JSON a plain tagged union (no per-type socket).
type FrameType string

const (
	FrameHello     FrameType = "hello"      // Go->UE handshake
	FrameWelcome   FrameType = "welcome"    // UE->Go handshake reply
	FrameRPC       FrameType = "rpc"        // Go->UE op invocation
	FrameRPCResult FrameType = "rpc_result" // UE->Go op result (same envelope as uexec, no marker)
	FrameProgress  FrameType = "progress"   // UE->Go long-op progress
	FrameEvent     FrameType = "event"      // UE->Go pushed observation
	FrameControl   FrameType = "control"    // bidir out-of-band (cancel/stop/pause/approve/replay/…)
	FrameGate      FrameType = "gate"       // UE->Go: op parked pending human approval
)

// ControlKind is the sub-type of a control frame (§2.4 control row).
type ControlKind string

const (
	CtrlCancel      ControlKind = "cancel"       // cancel(op_id)
	CtrlPause       ControlKind = "pause"        // pause the agent
	CtrlStep        ControlKind = "step"         // single-step
	CtrlResume      ControlKind = "resume"       // resume
	CtrlStop        ControlKind = "stop"         // stop-switch
	CtrlSetPolicy   ControlKind = "set_policy"   // push a policy change
	CtrlApprove     ControlKind = "approve"      // approve(gate_id)
	CtrlDeny        ControlKind = "deny"         // deny(gate_id)
	CtrlFocusTarget ControlKind = "focus_target" // focus_target(gate_id) — select+pilot in editor
	CtrlReplayFrom  ControlKind = "replay_from"  // replay-from(seq)
)

// Frame is one length-prefixed JSON message. Fields are optional per Type; a flat
// struct (vs nested variants) keeps the wire format trivially interoperable with the
// C++ FJsonObject side and omitempty keeps frames minimal.
type Frame struct {
	Type FrameType `json:"type"`

	// hello (Go->UE)
	Token           string `json:"token,omitempty"`
	LastSeenSeq     uint64 `json:"last_seen_seq,omitempty"`
	KnownEpoch      string `json:"known_epoch,omitempty"`
	ProtocolVersion int    `json:"protocol_version,omitempty"`

	// welcome (UE->Go)
	SessionEpoch   string `json:"session_epoch,omitempty"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
	EngineVersion  string `json:"engine_version,omitempty"`
	Project        string `json:"project,omitempty"`

	// rpc (Go->UE) / rpc_result (UE->Go)
	OpID   string          `json:"op_id,omitempty"`
	Op     string          `json:"op,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	Intent string          `json:"intent,omitempty"`  // agent rationale -> feed headline (§3.4)
	TaskID string          `json:"task_id,omitempty"` // grouping
	Gate   bool            `json:"gate,omitempty"`    // Go->UE: park this op for human approval (§4.2)

	// rpc_result envelope (same shape as the uexec/Python result, no marker)
	OK        *bool           `json:"ok,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Code      string          `json:"code,omitempty"`
	Retryable bool            `json:"retryable,omitempty"`
	Traceback string          `json:"traceback,omitempty"`

	// event / progress (UE->Go) — carry the monotonic seq
	Seq       uint64          `json:"seq,omitempty"`
	TWall     float64         `json:"t_wall,omitempty"`
	TEngine   float64         `json:"t_engine,omitempty"`
	EventType string          `json:"event_type,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Dropped   uint64          `json:"dropped,omitempty"` // ring drop-oldest count since last emit (§6.2)

	// control (bidir)
	Control ControlKind     `json:"control,omitempty"`
	GateID  string          `json:"gate_id,omitempty"`
	FromSeq uint64          `json:"from_seq,omitempty"`
	Policy  json.RawMessage `json:"policy,omitempty"`

	// gate (UE->Go)
	Classification string          `json:"classification,omitempty"`
	ArgsHash       string          `json:"args_hash,omitempty"`
	Diff           json.RawMessage `json:"diff,omitempty"` // before->after of the target (§4.2)
}

var (
	// ErrFrameTooLarge guards the length prefix against a corrupt/hostile size.
	ErrFrameTooLarge = errors.New("cockpit: frame exceeds MaxFrameBytes")
	// ErrShortFrame is returned when the stream ends mid-frame.
	ErrShortFrame = errors.New("cockpit: short frame")
)

// WriteFrame writes f as a 4-byte big-endian length prefix followed by its JSON. It
// writes the header and body in one buffered Write so a frame is never torn on the
// wire by a concurrent writer at a higher layer (callers still serialize writes).
func WriteFrame(w io.Writer, f *Frame) error {
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(body) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(body)))
	copy(buf[4:], body)
	_, err = w.Write(buf)
	return err
}

// ReadFrame reads one length-prefixed JSON frame. It bounds the length before
// allocating, so a garbage prefix cannot exhaust memory.
func ReadFrame(r io.Reader) (*Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, ErrShortFrame
	}
	if n > MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, ErrShortFrame
		}
		return nil, err
	}
	var f Frame
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, err
	}
	return &f, nil
}
