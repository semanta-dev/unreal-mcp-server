package uexec

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageEncodeNoHTMLEscape(t *testing.T) {
	// Embedded Python contains <, >, & — must round-trip literally, not <.
	code := `if a < b and c > d and e & f: print("<hi>")`
	data, _ := marshalNoEscape(commandData{Command: code, Unattended: true, ExecMode: string(ModeExecFile)})
	raw, err := newMessage(TypeCommand, "src", "dst", data).encode()
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	// The bad outcome is the ESCAPED form (< etc.); literal <, >, & are correct.
	for _, esc := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(s, esc) {
			t.Fatalf("HTML escape %s leaked into wire bytes: %s", esc, s)
		}
	}
	if !strings.Contains(s, "a < b and c > d and e & f") {
		t.Fatalf("literal Python not preserved: %s", s)
	}
	if strings.HasSuffix(s, "\n") {
		t.Fatalf("trailing newline should be trimmed: %q", s)
	}
}

func TestMessagePingOmitsDestAndData(t *testing.T) {
	raw, err := newMessage(TypePing, "me", "", nil).encode()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["dest"]; ok {
		t.Fatalf("ping must omit dest: %s", raw)
	}
	if _, ok := m["data"]; ok {
		t.Fatalf("ping must omit data: %s", raw)
	}
	if m["magic"] != ProtocolMagic || int(m["version"].(float64)) != ProtocolVersion {
		t.Fatalf("bad magic/version: %s", raw)
	}
}

func TestMessageRoundTrip(t *testing.T) {
	data, _ := json.Marshal(openConnData{CommandIP: "127.0.0.1", CommandPort: 6776})
	raw, _ := newMessage(TypeOpenConnection, "a", "b", data).encode()
	got, err := decodeMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeOpenConnection || got.Source != "a" || got.Dest != "b" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	var d openConnData
	if err := json.Unmarshal(got.Data, &d); err != nil || d.CommandPort != 6776 {
		t.Fatalf("data round trip failed: %v %+v", err, d)
	}
}

func TestMessageValidateRejects(t *testing.T) {
	if _, err := decodeMessage([]byte(`{"version":2,"magic":"ue_py","type":"pong","source":"x"}`)); err == nil {
		t.Fatal("expected version rejection")
	}
	if _, err := decodeMessage([]byte(`{"version":1,"magic":"nope","type":"pong","source":"x"}`)); err == nil {
		t.Fatal("expected magic rejection")
	}
}

func TestPassesFilter(t *testing.T) {
	cases := []struct {
		source, dest, self string
		want               bool
	}{
		{"editor", "", "me", true},       // broadcast from other
		{"editor", "me", "me", true},     // addressed to us
		{"me", "", "me", false},          // from ourselves
		{"editor", "other", "me", false}, // addressed elsewhere
	}
	for _, c := range cases {
		m := Message{Source: c.source, Dest: c.dest}
		if got := m.passesFilter(c.self); got != c.want {
			t.Errorf("passesFilter(src=%q dest=%q self=%q)=%v want %v", c.source, c.dest, c.self, got, c.want)
		}
	}
}
