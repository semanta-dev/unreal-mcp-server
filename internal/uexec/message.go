package uexec

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Message is a remote-exec protocol message (UDP or TCP), UTF-8 JSON on the wire.
type Message struct {
	Version int             `json:"version"`
	Magic   string          `json:"magic"`
	Type    MsgType         `json:"type"`
	Source  string          `json:"source"`
	Dest    string          `json:"dest,omitempty"` // omit when broadcasting (mirrors Python "include only if truthy")
	Data    json.RawMessage `json:"data,omitempty"` // deferred decode into a typed payload
}

// payload structs (message-specific data).
type openConnData struct {
	CommandIP   string `json:"command_ip"`
	CommandPort int    `json:"command_port"`
}

type commandData struct {
	Command    string `json:"command"`
	Unattended bool   `json:"unattended"`
	ExecMode   string `json:"exec_mode"`
}

// CommandResult is the editor's reply to a command (see command_result in the protocol).
type CommandResult struct {
	Success bool          `json:"success"`
	Result  string        `json:"result"`
	Output  []OutputEntry `json:"output"`
}

// OutputEntry is one captured log line from the editor. Type ∈ {Info,Warning,Error}.
type OutputEntry struct {
	Type   string `json:"type"`
	Output string `json:"output"`
}

// marshalNoEscape marshals a payload to JSON WITHOUT HTML escaping (so embedded
// Python source keeps literal <, >, &). Message.encode() also disables HTML
// escaping, but it operates on the already-serialized Data bytes — so the inner
// payload must be built here to stay byte-faithful end to end (§6.1).
func marshalNoEscape(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

func newMessage(t MsgType, source, dest string, data json.RawMessage) Message {
	return Message{
		Version: ProtocolVersion,
		Magic:   ProtocolMagic,
		Type:    t,
		Source:  source,
		Dest:    dest,
		Data:    data,
	}
}

// encode serializes the message to UTF-8 JSON bytes WITHOUT HTML escaping, so
// embedded Python source (which contains <, >, &) round-trips byte-faithfully.
// No trailing newline (json.Encoder appends one; we trim it) to stay wire-faithful.
func (m Message) encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// validate enforces the protocol invariants required before trusting a message.
func (m Message) validate() error {
	if m.Version != ProtocolVersion {
		return fmt.Errorf("version %d != %d", m.Version, ProtocolVersion)
	}
	if m.Magic != ProtocolMagic {
		return fmt.Errorf("magic %q != %q", m.Magic, ProtocolMagic)
	}
	return nil
}

// passesFilter reports whether this message should be received by the local node:
// not sent by ourselves, and either broadcast (no dest) or addressed to us.
func (m Message) passesFilter(self string) bool {
	return m.Source != self && (m.Dest == "" || m.Dest == self)
}

// decodeMessage parses and validates a single datagram (UDP path).
func decodeMessage(b []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if err := m.validate(); err != nil {
		return m, err
	}
	return m, nil
}
