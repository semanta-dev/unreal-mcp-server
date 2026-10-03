package session

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTeardownRunsOnceInReverseOrder(t *testing.T) {
	s := NewState("s1")
	var order []int
	s.OnTeardown(func() { order = append(order, 1) })
	s.OnTeardown(func() { order = append(order, 2) })
	s.Teardown()
	s.Teardown()
	if len(order) != 2 || order[0] != 2 || order[1] != 1 {
		t.Fatalf("order = %v, want [2 1] once", order)
	}
	late := false
	s.OnTeardown(func() { late = true })
	if !late || !s.Closed() {
		t.Fatal("a hook registered after teardown must run immediately")
	}
}

func TestStateAndDepsInContext(t *testing.T) {
	ctx := WithState(WithDeps(context.Background(), Deps{ProjectDir: "/p"}), NewState("x"))
	if st, ok := StateFrom(ctx); !ok || st.ID() != "x" {
		t.Fatal("state lost")
	}
	if d, ok := From(ctx); !ok || d.ProjectDir != "/p" {
		t.Fatal("deps lost")
	}
}

func TestProjectKey(t *testing.T) {
	dir := t.TempDir()
	up := filepath.Join(dir, "Game.uproject")
	if err := os.WriteFile(up, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if ProjectKey(dir) != ProjectKey(up) {
		t.Fatalf("dir and .uproject must share a key: %q vs %q", ProjectKey(dir), ProjectKey(up))
	}
	if ProjectKey(dir) != ProjectKey(dir+string(filepath.Separator)+".") {
		t.Fatal("cleaning not applied")
	}
	if runtime.GOOS == "windows" && ProjectKey(dir) != ProjectKey(strings.ToUpper(dir)) {
		t.Fatal("case must not matter on Windows")
	}
	if ProjectKey("") != "" {
		t.Fatal("empty stays empty")
	}
}

func TestLoadProjectFile(t *testing.T) {
	dir := t.TempDir()
	if pf, err := LoadProjectFile(dir); err != nil || len(pf.Toolsets) != 0 {
		t.Fatalf("missing file must be zero, nil: %+v %v", pf, err)
	}
	os.WriteFile(filepath.Join(dir, ProjectFileName), []byte(`{"toolsets":["ui","polyworld"],"gate_policy":"require"}`), 0o644)
	pf, err := LoadProjectFile(filepath.Join(dir, "Game.uproject"))
	if err != nil || len(pf.Toolsets) != 2 || pf.GatePolicy != "require" {
		t.Fatalf("got %+v %v", pf, err)
	}
	os.WriteFile(filepath.Join(dir, ProjectFileName), []byte(`{"gate_policy":"sometimes"}`), 0o644)
	if _, err := LoadProjectFile(dir); err == nil {
		t.Fatal("invalid gate_policy must error")
	}
}
