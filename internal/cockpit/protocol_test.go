package cockpit

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	ok := true
	frames := []*Frame{
		{Type: FrameHello, Token: "tok", LastSeenSeq: 42, KnownEpoch: "ep1", ProtocolVersion: ProtocolVersion},
		{Type: FrameWelcome, SessionEpoch: "ep2", ManifestDigest: "d1", EngineVersion: "5.7", Project: "/p"},
		{Type: FrameRPC, OpID: "op-1", Op: "list_actors", Args: json.RawMessage(`{"limit":10}`), Intent: "survey", TaskID: "t1"},
		{Type: FrameRPCResult, OpID: "op-1", OK: &ok, Result: json.RawMessage(`{"count":3}`)},
		{Type: FrameProgress, OpID: "op-2", Seq: 7, Payload: json.RawMessage(`{"pct":50}`)},
		{Type: FrameEvent, Seq: 8, TWall: 1.5, EventType: "pie.begin", Payload: json.RawMessage(`{}`), Dropped: 2},
		{Type: FrameControl, Control: CtrlStop},
		{Type: FrameControl, Control: CtrlApprove, GateID: "g1"},
		{Type: FrameControl, Control: CtrlReplayFrom, FromSeq: 5},
		{Type: FrameGate, GateID: "g2", OpID: "op-3", Classification: "destructive", ArgsHash: "abc", Diff: json.RawMessage(`{"Mass":{"before":100,"after":0}}`)},
	}
	var buf bytes.Buffer
	for _, f := range frames {
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatalf("WriteFrame %s: %v", f.Type, err)
		}
	}
	for i, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame #%d: %v", i, err)
		}
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		if string(wb) != string(gb) {
			t.Fatalf("frame #%d round-trip mismatch:\n want %s\n  got %s", i, wb, gb)
		}
	}
	if _, err := ReadFrame(&buf); err != io.EOF {
		t.Fatalf("expected EOF after last frame, got %v", err)
	}
}

func TestReadFrameRejectsOversizeWithoutAllocating(t *testing.T) {
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxFrameBytes+1)
	buf.Write(hdr[:])
	if _, err := ReadFrame(&buf); err != ErrFrameTooLarge {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestWriteFrameRejectsOversize(t *testing.T) {
	big := make([]byte, MaxFrameBytes) // JSON string of it exceeds the cap
	f := &Frame{Type: FrameEvent, Payload: json.RawMessage(`"` + string(big[:0]) + `"`)}
	f.Error = string(make([]byte, MaxFrameBytes+10))
	if err := WriteFrame(io.Discard, f); err != ErrFrameTooLarge {
		t.Fatalf("expected ErrFrameTooLarge on oversize write, got %v", err)
	}
}

func TestReadFrameShortBody(t *testing.T) {
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 100) // claims 100 bytes
	buf.Write(hdr[:])
	buf.Write([]byte(`{"type":"event"`)) // but supplies fewer
	if _, err := ReadFrame(&buf); err != ErrShortFrame {
		t.Fatalf("expected ErrShortFrame, got %v", err)
	}
}

func TestReadFrameZeroLength(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 0})
	if _, err := ReadFrame(&buf); err != ErrShortFrame {
		t.Fatalf("expected ErrShortFrame for zero-length frame, got %v", err)
	}
}
