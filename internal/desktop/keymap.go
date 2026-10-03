package desktop

import (
	"fmt"
	"strings"
)

// vkey is a Windows virtual-key code plus whether it is an "extended" key
// (navigation cluster, arrows, right-hand modifiers, numpad divide, etc.),
// which SendInput must flag with KEYEVENTF_EXTENDEDKEY to disambiguate.
type vkey struct {
	code     uint16
	extended bool
}

// KeyEvent is one raw key transition fed to the platform SendInput backend.
type KeyEvent struct {
	VK       uint16
	Up       bool
	Extended bool
}

// keyNames maps friendly key names (lowercase) to virtual-key codes. Kept in a
// build-tag-free file so chord parsing is unit-tested on every platform; the
// numeric VK constants are just data. Letters and digits are added
// programmatically in init.
var keyNames = map[string]vkey{
	"enter": {0x0D, false}, "return": {0x0D, false},
	"tab": {0x09, false}, "space": {0x20, false}, "spacebar": {0x20, false},
	"backspace": {0x08, false}, "back": {0x08, false},
	"escape": {0x1B, false}, "esc": {0x1B, false},
	"delete": {0x2E, true}, "del": {0x2E, true},
	"insert": {0x2D, true}, "ins": {0x2D, true},
	"home": {0x24, true}, "end": {0x23, true},
	"pageup": {0x21, true}, "pgup": {0x21, true},
	"pagedown": {0x22, true}, "pgdn": {0x22, true},
	"up": {0x26, true}, "down": {0x28, true}, "left": {0x25, true}, "right": {0x27, true},
	// Modifiers (generic, left-hand codes).
	"shift": {0x10, false}, "ctrl": {0x11, false}, "control": {0x11, false},
	"alt": {0x12, false}, "menu": {0x12, false},
	"win": {0x5B, true}, "super": {0x5B, true}, "cmd": {0x5B, true}, "meta": {0x5B, true},
	"lwin": {0x5B, true}, "rwin": {0x5C, true},
	// Locks / system.
	"capslock": {0x14, false}, "numlock": {0x90, false}, "scrolllock": {0x91, false},
	"printscreen": {0x2C, false}, "prtsc": {0x2C, false}, "pause": {0x13, false},
	"apps": {0x5D, true}, "menukey": {0x5D, true},
	// Numpad.
	"numpad0": {0x60, false}, "numpad1": {0x61, false}, "numpad2": {0x62, false},
	"numpad3": {0x63, false}, "numpad4": {0x64, false}, "numpad5": {0x65, false},
	"numpad6": {0x66, false}, "numpad7": {0x67, false}, "numpad8": {0x68, false},
	"numpad9":  {0x69, false},
	"multiply": {0x6A, false}, "add": {0x6B, false}, "subtract": {0x6D, false},
	"decimal": {0x6E, false}, "divide": {0x6F, true},
	// Common OEM punctuation (US layout) — handy in the odd shortcut.
	"plus": {0xBB, false}, "minus": {0xBD, false}, "comma": {0xBC, false},
	"period": {0xBE, false}, "tilde": {0xC0, false}, "grave": {0xC0, false},
	// VK_OEM_2 (0xBF) is the main-row '/', NOT an extended key — only numpad
	// VK_DIVIDE (0x6F, "divide" above) is extended.
	"semicolon": {0xBA, false}, "slash": {0xBF, false}, "backslash": {0xDC, false},
	"lbracket": {0xDB, false}, "rbracket": {0xDD, false}, "quote": {0xDE, false},
}

func init() {
	for c := 'a'; c <= 'z'; c++ {
		keyNames[string(c)] = vkey{uint16(0x41 + (c - 'a')), false}
	}
	for d := '0'; d <= '9'; d++ {
		keyNames[string(d)] = vkey{uint16(0x30 + (d - '0')), false}
	}
	for i := 1; i <= 24; i++ {
		keyNames[fmt.Sprintf("f%d", i)] = vkey{uint16(0x70 + (i - 1)), false}
	}
}

// lookupKey resolves one key token (already trimmed) to its virtual key.
func lookupKey(name string) (vkey, error) {
	k, ok := keyNames[strings.ToLower(name)]
	if !ok {
		return vkey{}, fmt.Errorf("unknown key %q", name)
	}
	return k, nil
}

// chordEvents parses a chord like "ctrl+shift+s", "F5", "alt+f4", "escape" into
// the ordered press/release KeyEvents that synthesize it: modifiers down in
// order, the main key down then up, modifiers up in reverse. A bare "+"
// (e.g. "ctrl++" to press the plus key) is handled by treating a trailing empty
// token as the literal "plus"/"+" key.
func chordEvents(chord string) ([]KeyEvent, error) {
	parts := splitChord(chord)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty key chord")
	}
	mods := parts[:len(parts)-1]
	main := parts[len(parts)-1]

	events := make([]KeyEvent, 0, (len(mods)*2)+2)
	modKeys := make([]vkey, len(mods))
	for i, m := range mods {
		mk, err := lookupKey(m)
		if err != nil {
			return nil, err
		}
		modKeys[i] = mk
		events = append(events, KeyEvent{VK: mk.code, Extended: mk.extended})
	}
	mainKey, err := lookupKey(main)
	if err != nil {
		return nil, err
	}
	events = append(events,
		KeyEvent{VK: mainKey.code, Extended: mainKey.extended},
		KeyEvent{VK: mainKey.code, Extended: mainKey.extended, Up: true})
	for i := len(modKeys) - 1; i >= 0; i-- {
		events = append(events, KeyEvent{VK: modKeys[i].code, Extended: modKeys[i].extended, Up: true})
	}
	return events, nil
}

// splitChord splits on '+' but preserves a literal trailing '+' as "plus", so
// "ctrl++" -> ["ctrl","plus"] and "+" -> ["plus"].
func splitChord(chord string) []string {
	chord = strings.TrimSpace(chord)
	if chord == "+" {
		return []string{"plus"}
	}
	// A trailing "++" means the last key is literally '+'.
	trailingPlus := strings.HasSuffix(chord, "++")
	raw := strings.Split(chord, "+")
	out := make([]string, 0, len(raw))
	for i, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			// empty segment from "++": only meaningful as the trailing plus key
			if trailingPlus && i == len(raw)-1 {
				out = append(out, "plus")
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// chordSequence parses a whitespace-or-comma-separated list of chords into a
// flat event stream, e.g. "ctrl+s enter" or "down, down, enter".
func chordSequence(chords []string) ([]KeyEvent, error) {
	var events []KeyEvent
	for _, c := range chords {
		ev, err := chordEvents(c)
		if err != nil {
			return nil, err
		}
		events = append(events, ev...)
	}
	return events, nil
}
