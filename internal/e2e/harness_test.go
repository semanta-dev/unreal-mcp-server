package e2e

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/app"
	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/bridge/bridgetest"
	"github.com/jdziat/unreal-mcp-server/internal/jobs"
	"github.com/jdziat/unreal-mcp-server/internal/tools"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
	"github.com/jdziat/unreal-mcp-server/internal/uexec/uexectest"
)

// harness is one fake editor + emulator + full MCP server + connected client.
type harness struct {
	specs  map[string]*spec.Spec // tool name -> spec (dynamic tier lint)
	bridge *bridge.Bridge
	editor *uexectest.Editor // nil when started with noEditor
	emu    *bridgetest.Emulator
	world  *bridgetest.World
	cs     *mcp.ClientSession
}

type harnessOpts struct {
	noEditor bool                     // discovery targets a dead port: no editor answers
	fake     func(*uexectest.Options) // fault-injection tweaks on the wire fake
	cfg      func(*uexec.Config)
	project  string         // Deps.ProjectDir (offline tools)
	toolsets []spec.Toolset // enabled at session start in addition to core
}

func testUexecConfig() uexec.Config {
	c := uexec.DefaultConfig()
	c.CommandAddr = "127.0.0.1:0"
	c.PingInterval = 50 * time.Millisecond
	c.NodeTimeout = 3 * time.Second
	c.DiscoveryTimeout = 3 * time.Second
	c.CommandTimeout = 5 * time.Second
	c.AcceptAttempts = 4
	c.AcceptTimeout = 400 * time.Millisecond
	return c
}

func startHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h := &harness{emu: bridgetest.New(), world: bridgetest.NewWorld()}
	h.world.Install(h.emu)

	var target net.Addr
	if o.noEditor {
		// A bound-but-silent UDP socket: pings go nowhere useful, no pong ever comes.
		pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pc.Close() })
		target = pc.LocalAddr()
	} else {
		fo := h.emu.Options()
		if o.fake != nil {
			o.fake(&fo)
		}
		ed, err := uexectest.Start(fo)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(ed.Close)
		h.editor = ed
		target = ed.Addr()
	}

	cfg := testUexecConfig()
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	disc, err := uexec.OpenUnicastDiscovery(ctx, cfg, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disc.Close() })
	sess := uexec.NewOnDiscovery(cfg, disc, nil)
	t.Cleanup(func() { sess.Close() })

	b := bridge.New(sess, bridge.Options{})
	h.bridge = b
	deps := tools.Deps{Bridge: b, Jobs: jobs.NewRegistry(), ProjectDir: o.project}
	srv := app.NewServer(app.Options{Deps: deps, Toolsets: o.toolsets}, nil).MCP
	h.specs = map[string]*spec.Spec{}
	for _, sp := range tools.Specs(deps) {
		h.specs[sp.Name] = sp
	}

	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	h.cs = cs
	return h
}

// call invokes a tool and fails the test on a protocol-level error (tool-level
// errors come back as IsError results and are returned for inspection).
func (h *harness) call(t *testing.T, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before := len(h.emu.Calls())
	res, err := h.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: protocol error: %v", name, err)
	}
	h.checkReaches(t, name, args, h.emu.Calls()[before:])
	return res
}

// checkReaches is the dynamic half of the tier lint (plan §2.1): every Python op a
// call actually dispatched must be declared in its OpSpec.Reaches. Specs that
// declare no Reaches (the v1 adapter) are not checked.
func (h *harness) checkReaches(t *testing.T, tool string, args map[string]any, observed []string) {
	t.Helper()
	sp, ok := h.specs[tool]
	if !ok {
		return
	}
	opName, _ := args["op"].(string)
	for _, op := range sp.Ops {
		if op.Name != opName || len(op.Reaches) == 0 {
			continue
		}
		declared := map[string]bool{}
		for _, r := range op.Reaches {
			declared[r] = true
		}
		for _, o := range observed {
			if !declared[o] {
				t.Errorf("%s op=%q dispatched python op %q not declared in Reaches %v", tool, opName, o, op.Reaches)
			}
		}
	}
}

// text concatenates a result's text content.
func text(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// structured decodes a result's structured content into a map.
func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structured content is not an object: %s", raw)
	}
	return out
}

// errorOf returns the structured envelope error of an isError result.
func errorOf(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected an error result, got: %s", text(res))
	}
	e, ok := structured(t, res)["error"].(map[string]any)
	if !ok {
		t.Fatalf("error result lacks structured error: %s", text(res))
	}
	for _, k := range []string{"code", "message", "retryable", "outcome"} {
		if _, ok := e[k]; !ok {
			t.Fatalf("envelope error missing %q: %v", k, e)
		}
	}
	return e
}
