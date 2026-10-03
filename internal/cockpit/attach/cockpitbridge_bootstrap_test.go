package attach

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/cockpit"
)

type fakeSelectable struct {
	info    json.RawMessage
	callErr error

	mu        sync.Mutex
	nativeSet bool
}

func (f *fakeSelectable) Call(_ context.Context, op string, _ any) (json.RawMessage, error) {
	if f.callErr != nil {
		return nil, f.callErr
	}
	if op == "cockpit_info" {
		return f.info, nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeSelectable) SetNative(n bridge.NativeDispatcher) {
	f.mu.Lock()
	f.nativeSet = n != nil
	f.mu.Unlock()
}

func (f *fakeSelectable) wasNativeSet() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nativeSet
}

func TestBootstrapSelectsNativeWhenPresent(t *testing.T) {
	ok := true
	addr := tinyEditor(t, func(string) *cockpit.Frame {
		return &cockpit.Frame{Type: cockpit.FrameRPCResult, OK: &ok, Result: json.RawMessage(`{}`)}
	})
	_, port, _ := net.SplitHostPort(addr)
	info := json.RawMessage(`{"cockpit_port":` + port + `,"session_epoch":"ep-1","token":"t","protocol_version":1}`)
	fs := &fakeSelectable{info: info}

	store := NewMemEpochStore()
	sess, err := Bootstrap(context.Background(), fs, BootstrapConfig{
		Project: "aesir", CockpitToken: "c", DialTimeout: 2 * time.Second, Epochs: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess == nil {
		t.Fatal("expected a cockpit session when MCPCore is present")
	}
	t.Cleanup(func() { sess.Close() })
	if !fs.wasNativeSet() {
		t.Fatal("SetNative should have selected the framed backend")
	}
	if sess.EditorEpoch() != "ep-1" {
		t.Fatalf("epoch = %q", sess.EditorEpoch())
	}
	if store.epochs["aesir"] != "ep-1" {
		t.Fatal("epoch not recorded")
	}
}

func TestBootstrapStaysUexecWhenAbsent(t *testing.T) {
	fs := &fakeSelectable{info: json.RawMessage(`{"cockpit":"not_present"}`)}
	sess, err := Bootstrap(context.Background(), fs, BootstrapConfig{Project: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if sess != nil {
		t.Fatal("expected nil session on the uexec fallback")
	}
	if fs.wasNativeSet() {
		t.Fatal("SetNative must NOT be called when native is absent")
	}
}

func TestBootstrapSurfacesProbeError(t *testing.T) {
	fs := &fakeSelectable{callErr: errors.New("uexec channel dead")}
	if _, err := Bootstrap(context.Background(), fs, BootstrapConfig{}); err == nil {
		t.Fatal("a probe transport error must surface, not read as not-present")
	}
}

func TestMemEpochStoreChangeDetection(t *testing.T) {
	s := NewMemEpochStore()
	s.Record("p", "ep-1")
	if s.EpochChanged("p", "ep-1") {
		t.Fatal("same epoch should not be 'changed'")
	}
	if !s.EpochChanged("p", "ep-2") {
		t.Fatal("a new epoch should be detected as changed (editor/MCPCore reset)")
	}
}
