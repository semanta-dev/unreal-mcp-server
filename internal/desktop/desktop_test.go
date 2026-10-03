package desktop

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestChordEvents(t *testing.T) {
	cases := []struct {
		chord string
		want  []KeyEvent // VK + Up in order
	}{
		{"a", []KeyEvent{{VK: 0x41}, {VK: 0x41, Up: true}}},
		{"F5", []KeyEvent{{VK: 0x74}, {VK: 0x74, Up: true}}},
		{"ctrl+s", []KeyEvent{
			{VK: 0x11}, {VK: 0x53}, {VK: 0x53, Up: true}, {VK: 0x11, Up: true},
		}},
		{"ctrl+shift+p", []KeyEvent{
			{VK: 0x11}, {VK: 0x10}, {VK: 0x50}, {VK: 0x50, Up: true}, {VK: 0x10, Up: true}, {VK: 0x11, Up: true},
		}},
	}
	for _, c := range cases {
		got, err := chordEvents(c.chord)
		if err != nil {
			t.Fatalf("chordEvents(%q): %v", c.chord, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("chordEvents(%q): got %d events, want %d (%+v)", c.chord, len(got), len(c.want), got)
		}
		for i := range got {
			if got[i].VK != c.want[i].VK || got[i].Up != c.want[i].Up {
				t.Errorf("chordEvents(%q)[%d] = %+v, want %+v", c.chord, i, got[i], c.want[i])
			}
		}
	}
}

func TestChordExtendedFlag(t *testing.T) {
	got, err := chordEvents("delete")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Extended {
		t.Fatalf("delete should be an extended key: %+v", got)
	}
	// Arrow keys are extended too.
	up, _ := chordEvents("up")
	if !up[0].Extended {
		t.Errorf("up arrow should be extended: %+v", up)
	}
}

// TestExtendedKeyFlags locks the extended/non-extended distinction that
// SendInput relies on: numpad divide is extended, main-row slash is not; arrows
// and the nav cluster are extended, letters are not.
func TestExtendedKeyFlags(t *testing.T) {
	extended := []string{"divide", "up", "down", "left", "right", "home", "end", "delete", "insert", "pageup", "pagedown", "win", "apps"}
	notExtended := []string{"slash", "a", "z", "0", "9", "f5", "enter", "space", "shift", "ctrl", "alt", "semicolon", "backslash"}
	for _, name := range extended {
		if k, err := lookupKey(name); err != nil || !k.extended {
			t.Errorf("%q should be extended (err=%v extended=%v)", name, err, k.extended)
		}
	}
	for _, name := range notExtended {
		if k, err := lookupKey(name); err != nil || k.extended {
			t.Errorf("%q should NOT be extended (err=%v extended=%v)", name, err, k.extended)
		}
	}
	// slash (main-row) and divide (numpad) share the glyph but differ in the flag.
	slash, _ := lookupKey("slash")
	div, _ := lookupKey("divide")
	if slash.code != 0xBF || slash.extended {
		t.Errorf("slash = %+v, want {0xBF false}", slash)
	}
	if div.code != 0x6F || !div.extended {
		t.Errorf("divide = %+v, want {0x6F true}", div)
	}
}

func TestSplitChordPlus(t *testing.T) {
	cases := map[string][]string{
		"ctrl+s": {"ctrl", "s"},
		"+":      {"plus"},
		"ctrl++": {"ctrl", "plus"},
		" F5 ":   {"F5"},
	}
	for in, want := range cases {
		got := splitChord(in)
		if len(got) != len(want) {
			t.Fatalf("splitChord(%q) = %v, want %v", in, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("splitChord(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

func TestChordUnknownKey(t *testing.T) {
	if _, err := chordEvents("ctrl+notakey"); err == nil {
		t.Error("expected error for unknown key")
	}
	if _, err := chordEvents(""); err == nil {
		t.Error("expected error for empty chord")
	}
}

func TestChordSequence(t *testing.T) {
	got, err := chordSequence([]string{"ctrl+a", "delete"})
	if err != nil {
		t.Fatal(err)
	}
	// ctrl+a => 4 events, delete => 2 events.
	if len(got) != 6 {
		t.Fatalf("got %d events, want 6: %+v", len(got), got)
	}
}

func TestPickWindow(t *testing.T) {
	wins := []Window{
		{HWND: 1, PID: 100, Title: "Untitled - Notepad", W: 800, H: 600},
		{HWND: 2, PID: 200, Title: "AesirWaveDefense - Unreal Editor", W: 1920, H: 1080, Foreground: true},
		{HWND: 3, PID: 200, Title: "Message", W: 10, H: 10},
		{HWND: 4, PID: 300, Title: "Something - Unreal Editor (tiny)", W: 100, H: 100},
	}

	// Auto picks the foreground, largest Unreal Editor window.
	if w, ok := pickWindow(wins, Selector{}); !ok || w.HWND != 2 {
		t.Errorf("auto: got %+v ok=%v, want hwnd=2", w, ok)
	}
	// By HWND.
	if w, ok := pickWindow(wins, Selector{HWND: 4}); !ok || w.HWND != 4 {
		t.Errorf("by hwnd: got %+v", w)
	}
	// By PID picks the larger window of that process.
	if w, ok := pickWindow(wins, Selector{PID: 200}); !ok || w.HWND != 2 {
		t.Errorf("by pid: got %+v, want hwnd=2", w)
	}
	// By title substring, case-insensitive.
	if w, ok := pickWindow(wins, Selector{Title: "notepad"}); !ok || w.HWND != 1 {
		t.Errorf("by title: got %+v", w)
	}
	// No match.
	if _, ok := pickWindow(wins, Selector{Title: "does-not-exist"}); ok {
		t.Error("expected no match")
	}
	// Missing HWND is a hard miss (not a fallthrough to auto).
	if _, ok := pickWindow(wins, Selector{HWND: 999}); ok {
		t.Error("unknown hwnd should not match")
	}
}

func TestDownscale(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for i := range src.Pix {
		if i%4 == 3 {
			src.Pix[i] = 255
		} else if i%4 == 0 {
			src.Pix[i] = 120 // R
		} else if i%4 == 1 {
			src.Pix[i] = 60 // G
		} else {
			src.Pix[i] = 30 // B
		}
	}
	dst := downscale(src, 100)
	if dst.Bounds().Dx() != 100 || dst.Bounds().Dy() != 50 {
		t.Fatalf("downscale dims = %dx%d, want 100x50", dst.Bounds().Dx(), dst.Bounds().Dy())
	}
	// A uniform image must survive box-averaging unchanged.
	c := dst.RGBAAt(50, 25)
	if c.R != 120 || c.G != 60 || c.B != 30 || c.A != 255 {
		t.Errorf("downscaled color = %+v, want {120 60 30 255}", c)
	}
	// No-op when already within bounds.
	if got := downscale(src, 800); got != src {
		t.Error("downscale should return src unchanged when already small enough")
	}
}

func TestEncodePNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 32), G: uint8(y * 32), B: 128, A: 255})
		}
	}
	data, err := encodePNG(img)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if decoded.Bounds() != img.Bounds() {
		t.Errorf("roundtrip bounds = %v, want %v", decoded.Bounds(), img.Bounds())
	}
}

func TestButtonFlags(t *testing.T) {
	if d, u, err := buttonFlags(""); err != nil || d != mouseLeftDown || u != mouseLeftUp {
		t.Errorf("default button: %v %v %v", d, u, err)
	}
	if d, u, err := buttonFlags(MouseRight); err != nil || d != mouseRightDown || u != mouseRightUp {
		t.Errorf("right button: %v %v %v", d, u, err)
	}
	if _, _, err := buttonFlags("bogus"); err == nil {
		t.Error("expected error for bogus button")
	}
}
