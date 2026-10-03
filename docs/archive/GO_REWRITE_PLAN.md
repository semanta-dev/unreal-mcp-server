# Unified Plan — Go Rewrite of `unreal-mcp-server` as a Complete idea→verified-in-editor Solution for Unreal + Claude

*Synthesis of three architect drafts (protocol-migration, e2e-maximalist, go-architecture). Conflicts are resolved explicitly in §2. Ground truth was re-verified against `server.py`, `unreal_bridge.py`, and `remote_execution.py`. Revision 2 closes the two GameDev blockers (Phase-4 TCP port collision; `live_coding_compile` parity contradiction) and the CTO blocker (no written security/trust model), and folds in all reviewer improvements.*

---

## 1. Executive Summary

We replace the Python MCP server (`python.exe server.py` over stdio) with a single static Go binary, `unreal-mcp.exe`, that (a) faithfully re-implements Epic's UDP-multicast-discovery + TCP-reverse-connect remote-execution wire protocol, (b) serves MCP over stdio via the official Go SDK, and (c) drives the live UE 5.7 editor. Cutover is a one-line edit to `aesir-wave-defense/.mcp.json`; the Python server stays on disk untouched as an instant rollback.

Six load-bearing decisions carry the plan:

| # | Decision | One-line rationale |
|---|----------|--------------------|
| **D1** | **Official SDK `github.com/modelcontextprotocol/go-sdk/mcp`** | The client *is* Claude Code (Anthropic); spec-tracking SDK with generics-typed tools → free JSON Schema, structured output, image content, middleware. `mark3labs/mcp-go` is the documented fallback. |
| **D2** | **Companion in-editor Python module (`mcp_bridge.py`), hot-loaded over the wire via `go:embed`, invoked with base64-JSON args** | Go ships *data*, never hand-built code. Eliminates the entire string-injection / `pybool`/`pyfloat` / brace-escaping risk class. Adding a tool becomes a 3-file change. |
| **D3** | **`json.Decoder`-framed TCP, `x/net/ipv4` loopback multicast, single-flight mutex + connection-tainting** | Replaces Epic's fragile `recv < 8192` heuristic; correct, stream-safe cancellation for *uninterruptible* editor commands, with a proven-by-test timeout-recovery contract. |
| **D4** | **Beyond parity: own the full loop by v1.0** — build orchestration with parsed diagnostics + streamed progress, deterministic PIE observation, exposure-correct PIE capture, attributable logs, git checkpoints, editor lifecycle management | This is what makes "idea→verified unattended" real, not aspirational. Sequenced *after* a de-risked parity cutover so a working server is always available. |
| **D5** | **Pure-Go, `CGO_ENABLED=0` static exe; minimal dependency budget** | SDK + `x/net` + `x/sys` only. No venv, no runtime, no cobra/viper/zap/gopsutil/uuid dep. Cutover = one `.mcp.json` line. |
| **D6** | **`fakeeditor` protocol double + injectable transport seams** | ~90% of tests run in CI with no live UE; a `uspike` live smoke test (also carrying the `__main__`-persistence probe and node-by-project selection) is the go/no-go gate for the protocol port. |

The plan is phased so the **risky things happen first and cheaply**: a standalone protocol spike validates Windows loopback multicast in Go against the real editor — *and* proves cross-command `__main__` persistence — before any MCP code exists. Parity + cutover complete by Phase 5; the E2E pillars (build/PIE/logs/git) layer on afterward without regressing the shipped server.

---

## 2. Synthesis Decisions — Conflicts Resolved (do not paper over)

The three drafts genuinely disagreed on five load-bearing points. Resolutions:

**C1 — Snippet management: verbatim-embed (Draft A) vs companion dispatch module (Drafts B & C).**
*Resolved in favor of the companion module (D2), but the parity concern is honored by construction.* Draft A's fear — "an editor-side moving part and a Go↔module version-sync failure mode raises cutover risk" — is exactly what Draft C's **version sentinel** eliminates: the module self-heals (re-execs) whenever the embedded version constant ≠ the loaded one, so cold/stale sessions self-correct with zero human action. Crucially, we honor Draft A's "reuse tested logic byte-for-byte" by **relocating the existing, proven editor-side Python** (the `_find_actor` helper, the `Rotator(roll,pitch,yaw)` ordering, the SceneCapture2D export, the throttle-off trick) *verbatim* into `mcp_bridge.py` functions — the logic doesn't change, only where it lives. And Draft A's cutover-parity guarantee is delivered by the **side-by-side A/B parity harness** (Draft A's Phase 4), which proves the companion-module outputs equal the current Python server's outputs before we flip the switch. Net: base64-JSON args make Draft A's `pybool`/`pyfloat`/`_embed`-nil-vs-`null` formatter landmines **disappear entirely** (the editor does `json.loads`, so Python literal formatting is never generated in Go). This is a case where the better architecture also erases a whole risk category. The one remaining assumption under this design — that the editor's `__main__` namespace persists across commands so the hot-loaded module stays resident — is no longer left to faith: it is proven by a 2-command probe in the **P1 spike** (§8.2), and the default snippet mode is chosen from that result.

**C2 — MCP SDK: official (A, C) vs `mark3labs/mcp-go` (B).**
*Resolved: official SDK (D1),* on Draft C's rationale (spec-authoritative, generics-typed, the client is Anthropic's own tool). `mark3labs/mcp-go` — mature and image-capable — is the pinned-in-writing fallback if the official API churns. SDK usage is isolated behind `internal/tools` + `marshal.go` so a swap touches one layer.

**C3 — Scope of v1: parity-only (A) vs everything (B) vs parity+build (C).**
*Resolved: v1.0 is the complete E2E solution, but delivered in gated phases with parity+cutover first.* A pure-parity v1 fails the stated bar ("go beyond tool-parity… idea→verified without babysitting"). Draft B's everything-at-once risks an unshippable big-bang. So: Phases 1–5 deliver de-risked parity + cutover (CTO-pleasing: a working server always exists, single-line rollback); Phases 6–9 add build orchestration, PIE observation, logs, and git **on top of the shipped binary** (GameDev-pleasing: the full loop by v1.0). Deferred to v1.1+: C++ scaffolding generator, Blueprint graph authoring, Gauntlet/synthetic-input, perf capture, navmesh/lighting bake, full property auto-reflection, multi-project (justified in §16).

**C4 — Tool naming: freeze current flat names (A) vs `domain.verb` rename (B).**
*Resolved: the 16 parity names are frozen exactly* (`editor_status`, `spawn_actor`, …). Renaming them would break existing call sites in `aesir-wave-defense` and make the A/B parity diff meaningless. New tools follow a consistent flat `group_verb` snake_case convention (`build_compile`, `pie_observe`, `logs_since`, `git_checkpoint`), giving Draft B's logical grouping without Draft B's rename churn.

Parity tools may *additively* enrich their structured output (e.g. `editor_status` gains `editor_pid`, `bridge_version`) **only when the human-readable text content is unchanged**, so the A/B text diff stays meaningful. **`live_coding_compile` is a strict, unchanged parity tool** — it keeps today's exact fire-and-forget behavior and text ("Live Coding compile triggered (see editor log/Live Coding console for status)"), diffs verbatim in Phase 4, and does **not** block or tail logs. The richer, blocking "compile → tail log → return structured diagnostics" contract the GameDev loop needs lives in a **new** tool, `build_compile(strategy="livecoding")` (Group F), where changed timing and changed output shape are expected and specified — not smuggled into a frozen parity tool. This deliberately resolves the earlier contradiction: the parity rule and the cutover acceptance criterion no longer conflict for any tool.

**C5 — Dependencies & editor lifecycle: gopsutil + uuid (B) vs stdlib-minimal (C).**
*Resolved: Draft C's minimalism wins* — UUIDv4 via a ~10-line `crypto/rand` helper (no dep); PID liveness via `golang.org/x/sys/windows` `OpenProcess` (already a dep for `SO_REUSEADDR`), not gopsutil. But Draft B's **capability** — the server owning the editor's launch/quit/restart lifecycle — is kept (it's mandatory for full C++ rebuilds and crash recovery), just implemented on the minimal dep set.

Minor: module path `github.com/jdziat/unreal-mcp-server` (A, C); Go toolchain pinned to current stable at bootstrap (verify ≥ SDK's `go` directive; ≈1.26.x as of 2026‑07) with `GOTOOLCHAIN=local`.

---

## 3. Goals & Non-Goals

**Goals (v1.0):**
1. Byte-faithful port of the remote-exec protocol; interoperates with the *unmodified* editor plugin.
2. Behavioral parity for all 16 current tools (validated by an automated A/B diff harness).
3. Single static `unreal-mcp.exe`; no Python/venv needed to *run* the server.
4. The complete unattended loop: **build (with diagnostics + streamed progress) → open → author → PIE → observe → verify (state + visual + log) → checkpoint(git)**.
5. Reliability for long unattended sessions: reconnect-once, background rediscovery, connection tainting with a proven recovery contract, per-tool timeouts, cancellable jobs, editor crash detection + optional auto-relaunch.
6. Low-risk cutover with instant single-line rollback.
7. A **written, analyzed trust model** for an unattended arbitrary-Python executor (§12), with each item mitigated or knowingly accepted.

**Non-Goals (v1.0):** C++ class scaffolding generator; Blueprint *graph* authoring (game is C++-only; Python can't reliably author graphs); synthetic-input/Gauntlet automation; perf/CSV profiling; navmesh/lighting bake orchestration; full generic property auto-reflection in `pie_observe` (v1 uses a per-class allowlist — see §9/§10); multi-project/multi-editor; changing the editor plugin or engine. (See §16 for justification and where each lands.)

---

## 4. Current-State Analysis (verified against source)

- **`server.py`** — FastMCP, 16 tools. Each builds a Python snippet string with f-strings + `_embed()` (`json.dumps` or `"None"`) and `{{ }}` brace-doubling. **7 of the 16 tools use the `print("__MCP_JSON__" + json.dumps(...))` scrape convention via `bridge.run_json`** — `editor_status`, `list_actors`, `get_actor`, `spawn_actor`, `list_assets`, `import_assets`, and `take_screenshot` (which scrapes a file path then reads PNG bytes back). The other **9** return `bridge.format_output(bridge.run_python(...))` flattened text (`execute_python`, `execute_console_command`, `open_level`, `delete_actor`, `set_actor_transform`, `save_all`, `start_play`, `stop_play`, `live_coding_compile`). `take_screenshot` returns `Image(data, format="png")`. *(Verified 2026-07: `run_json` call sites at server.py lines 48/105/128/168/216/248/273; the earlier draft's "8" was a miscount.)*
- **`unreal_bridge.py`** — one cached `_CONNECTION`, a `threading.Lock` (`_LOCK`) serializing *all* commands, `_connect()` with a 5s discovery poll, reconnect-once (`for attempt in range(2)`), `UnrealNotRunning` is **not** retried. `run_python` prepends `"# mcp\n"` in `MODE_EXEC_FILE` to dodge the leading-quote mis-parse. `run_json` finds the first output entry containing `__MCP_JSON__` and parses the tail. `format_output` prefixes `[Error]`/`[Warning]`, appends `result` unless in `{None,"","None"}`, prepends `[Execution failed]` on `success==False`, else `"(no output)"`. `_connect` picks `nodes[0]` arbitrarily (no project filtering).
- **`remote_execution.py`** (Epic, vendored) — constants confirmed: version `1`, magic `ue_py`, group `239.0.0.1:6766`, bind `127.0.0.1`, command endpoint `127.0.0.1:6776`, TTL `0`, ping `1s`, node timeout `5s`, recv buffer `8192`. Command-result read uses the `recv(8192)` "stop when a chunk is `< 8192`" heuristic on **both** UDP (`_run_broadcast_listen_thread`, line ~288) and TCP (`run_command`, line ~463). Accept retry is `for _n in range(6)` × 5s (`_try_accept`, line ~493). `run_command(command, unattended=True, exec_mode=MODE_EXEC_FILE, raise_on_failure=False)`. The command listen socket is created **lazily per command** (not at `start()`), so a dormant client never holds `:6776` — a fact the migration plan (§15) relies on.
- **Known editor gotchas baked into snippets:** `spawn_actor` uses `unreal.Rotator({roll},{pitch},{yaw})` with `z: float = 100` default; `set_actor_transform` maps `rotation_pyr=[pitch,yaw,roll]` → `unreal.Rotator(rot[2],rot[0],rot[1])`. Backgrounded editor throttling; `EditorPerformanceSettings` not directly Python-exposed in 5.7 (CDO `load_class` trick). `take_screenshot` is SceneCapture2D (editor-world, backgrounded-safe, **not** valid during PIE; RESUME notes it misleads on exposure).
- **Consumer:** `aesir-wave-defense/.mcp.json` → `mcpServers.unreal = {command: .venv/Scripts/python.exe, args:[server.py]}`. UE 5.7 at `D:/Unreal/Engine/UE_5.7`. Build: `Build.bat AesirWaveDefenseEditor Win64 Development` (editor closed) or console `LiveCoding.Compile` (editor open). Idempotent `Scripts/*.py` level recipes. Go is **not installed** anywhere on PATH.

---

## 5. Target Architecture — Go Module & Package Layout

Module `github.com/jdziat/unreal-mcp-server` · binary `dist/unreal-mcp.exe` · `internal/` throughout (nothing is a public API; free to refactor). The old Python files remain at repo root **untouched** as the rollback artifact throughout migration.

```
unreal-mcp-server/
  go.mod  go.sum                    # go 1.26 (verify at bootstrap); GOTOOLCHAIN=local
  server.py unreal_bridge.py remote_execution.py   # UNTOUCHED — rollback path
  cmd/
    unreal-mcp/main.go              # wiring only: config → logger → uexec → bridge → build → server → serve
    uspike/main.go                  # Phase-1 protocol harness (go/no-go gate): live wire + __main__ probe + node pick; no MCP code
  internal/
    uexec/                          # THE PORT of remote_execution.py (protocol client)
      config.go                     #   endpoints/TTL/timeouts (mirrors RemoteExecutionConfig)
      message.go                    #   wire Message{version,magic,type,source,dest,data}; encode/decode + receive-filter
      discovery.go                  #   UDP multicast: ping loop, pong handling, node table, node-by-project pick, WaitForNode(ctx)
      command.go                    #   TCP reverse-connect: OpenCommand, RunCommand, json.Decoder framing, taint + recovery
      node.go                       #   nodeTable with 5s TTL; pong project/engine metadata
      client.go                     #   Session: Start/Stop, Nodes(), RunCommand(ctx,…)
      sockopt_windows.go            #   SO_REUSEADDR-before-bind via ListenConfig.Control; loopback iface
      seams.go                      #   packetConn/listener/dialer/clock interfaces for tests
    bridge/                         # higher-level semantics over uexec (port of unreal_bridge.py)
      bridge.go                     #   Bridge iface: RunPython, RunJSON, Eval, Call(op,args), FormatOutput
      install.go                    #   companion-module version sentinel + hot-load (or on-disk fallback)
      result.go                     #   CommandResult{Success,Result,Output[]}, OutputEntry{Type,Output}
    snippets/
      snippets.go                   #   //go:embed py/*.py ; Version() constant ; Source()
      py/mcp_bridge.py              #   THE companion module: _mcp_dispatch table + all structured ops + per-class observe allowlist
      py/_prelude.py                #   _find_actor, __MCP_JSON__ emit, rotator/compat helpers, throttle-off
    tools/
      register.go                   #   Register(s, deps); generic add[In,Out]()
      session.go actors.go level.go assets.go play.go screenshot.go build.go logs.go git.go lifecycle.go jobs.go
      middleware.go                 #   server receiving-middleware: logging + panic-recovery + timeout
      marshal.go                    #   resultToText, errToResult, readScreenshot (fs poll + ImageContent)
    build/                          # Go-native C++ orchestration (no editor round-trip)
      build.go                      #   exec.CommandContext: Build.bat / RunUAT; strategy classifier; streamed log-tail
      diag.go                       #   MSVC/UBT/UHT diagnostic parser (regex set)
    lifecycle/lifecycle.go          # launch/detect/quit/restart UnrealEditor.exe; PID liveness (x/sys/windows)
    jobs/jobs.go                    # async job registry (build_compile, pie_wait_until, editor_restart) + streamed progress
    config/config.go                # flags > env > defaults
    logging/logging.go              # slog → STDERR only; cages stdlib log
    mcperr/errors.go                # error taxonomy
  testdata/fakeeditor/              # editor-side protocol double (shared by all protocol tests)
  scripts/bootstrap.ps1 build.ps1
  .github/workflows/ci.yml
```

`go.mod` (dependency budget — D5):
```
module github.com/jdziat/unreal-mcp-server
go 1.26                                         // verify ≥ SDK min at bootstrap
require (
    github.com/modelcontextprotocol/go-sdk v0.x.y   // PIN exact; verify latest at impl time
    golang.org/x/net v0.x.0                          // ipv4 multicast control
    golang.org/x/sys v0.x.0                          // Windows SO_REUSEADDR + OpenProcess liveness
)
```
Everything else (UUIDv4, JSON, sockets, subprocess, flag parsing) is stdlib. Extensibility seams keep the layout god-object-free:
```go
type bridge.Bridge interface { Call(ctx, op string, args any) (json.RawMessage, error); RunPython(ctx, code string, mode ExecMode) (CommandResult, error); Eval(ctx, expr string) (string, error) }
type build.Runner  interface { Compile(ctx, strategy string, progress chan<- string) (Diagnostics, error) }
```

---

## 6. Remote-Execution Protocol Port — `internal/uexec`

Faithful re-implementation keeping every constant so it interoperates with the unmodified plugin. Constants become `const`/`var` in `uexec/config.go`; any deviation is a protocol bug.

| Constant | Value | Source |
|---|---|---|
| version / magic | `1` / `ue_py` | `_PROTOCOL_VERSION` / `_PROTOCOL_MAGIC` |
| types | `ping pong open_connection close_connection command command_result` | `_TYPE_*` |
| exec modes | `ExecuteFile ExecuteStatement EvaluateStatement` | `MODE_*` (must match engine `LexToString`) |
| multicast group | `239.0.0.1:6766` | `DEFAULT_MULTICAST_GROUP_ENDPOINT` |
| bind / command | `127.0.0.1` / `127.0.0.1:6776` | confirmed in DefaultEngine.ini + `DEFAULT_COMMAND_ENDPOINT` |
| TTL / ping / node timeout | `0` / `1s` / `5s` | `DEFAULT_MULTICAST_TTL` / `_NODE_PING_SECONDS` / `_NODE_TIMEOUT_SECONDS` |
| accept retry / discovery wait | `6 × 5s` / `5s` | `_try_accept` / `unreal_bridge._connect` |

### 6.1 Message types (Go)
```go
type Message struct {
    Version int             `json:"version"`
    Magic   string          `json:"magic"`
    Type    MsgType         `json:"type"`
    Source  string          `json:"source"`
    Dest    string          `json:"dest,omitempty"` // omit when broadcasting (ping); "" != included
    Data    json.RawMessage `json:"data,omitempty"` // deferred decode → typed payloads
}
func (m *Message) passesFilter(self string) bool { return m.Source != self && (m.Dest == "" || m.Dest == self) }

type openConnData  struct{ CommandIP string `json:"command_ip"`; CommandPort int `json:"command_port"` }
type commandData   struct{ Command string `json:"command"`; Unattended bool `json:"unattended"`; ExecMode string `json:"exec_mode"` }
type CommandResult struct{ Success bool `json:"success"`; Result string `json:"result"`; Output []OutputEntry `json:"output"` }
type OutputEntry   struct{ Type string `json:"type"`; Output string `json:"output"` } // Type ∈ {Info,Warning,Error}
```
Parity rules that bite: `omitempty` on `Dest`/`Data` reproduces Python's "include only if truthy" (a `"dest":""` on a ping would be filtered wrong editor-side); on decode reject `Version != 1 || Magic != "ue_py"` and apply `passesFilter`. Node id = `crypto/rand` UUIDv4 (helper, no dep).

**Encoding & the wire-diff claim (corrected).** Encode the `command` payload with `enc.SetEscapeHTML(false)` so the embedded Python source bytes are not HTML-escaped and round-trip exactly (Go otherwise escapes `<`, `>`, `&`). This makes the *payload string* byte-faithful to the Python client, but it does **not** make the full serialized message byte-identical: Go's `json.Marshal` uses compact separators (`,`/`:`), while the vendored Python `json.dumps` defaults to `, `/`: ` (with spaces), so inter-field whitespace differs. The editor's `json.loads` accepts either encoding, so **functional parity holds** — but the earlier "clean byte diff" framing was wrong. The Phase-4 wire-diff test therefore compares **semantically**: it re-parses each captured `ping`/`open_connection`/`command` message on both sides to a canonical Go `map`/struct and asserts deep equality (optionally re-serializing both through the same canonical encoder for a normalized string diff). `SetEscapeHTML(false)` is retained purely for embedded-Python fidelity, not for byte-for-byte equality.

### 6.2 Framing — the sharp UDP/TCP distinction
Epic uses one `recv(8192)`-until-short-read heuristic for both. That is **correct for UDP, latently broken for TCP**:
- **UDP (discovery):** one `ReadFrom` == exactly one datagram → parse `buf[:n]` directly into a 64 KiB buffer. **Do not** port the accumulation loop (it would concatenate two datagrams).
- **TCP (command channel):** the short-read heuristic **deadlocks** when a `command_result` is an exact multiple of 8192 bytes, and risks corruption on large `list_actors`/log spew. Because the channel is strictly one-request→one-response, single-flight, with no unsolicited server messages, bind **one long-lived `*json.Decoder`** to the conn for its lifetime:
  ```go
  c.dec = json.NewDecoder(bufio.NewReader(c.conn)) // created once at accept
  var m Message; err := c.dec.Decode(&m)           // reads exactly one JSON value; buffers surplus
  ```
  JSON values are self-delimiting → immune to segmentation and exact-multiple sizes. Send side is `c.conn.Write(bytes)` (no length prefix), identical to Python `sendall`. This is a deliberate, low-risk improvement, justified by the strict req/resp protocol.

### 6.3 Windows socket options — the make-or-break details
Both ports are ones the editor also holds → `SO_REUSEADDR` **before bind** via `ListenConfig.Control` (Go's `net` won't set it; Windows has no `REUSEPORT` → `REUSEADDR` is the correct branch):
```go
func reuseAddr(_, _ string, c syscall.RawConn) (serr error) {
    c.Control(func(fd uintptr) { serr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1) })
    return
}
var lc = net.ListenConfig{Control: reuseAddr}
```
UDP multicast bring-up mirrors `_init_broadcast_socket` **exactly**, and the loopback interface must be passed **explicitly** (with TTL 0, default egress on the physical NIC means pings never reach the editor even though everything "looks" wired):
```go
pc, _ := lc.ListenPacket(ctx, "udp4", "127.0.0.1:6766")
u4 := ipv4.NewPacketConn(pc)
lo := loopbackInterface()                                  // scans net.Interfaces() for FlagLoopback / 127.0.0.1
u4.SetMulticastInterface(lo)                               // IP_MULTICAST_IF = loopback  (mandatory)
u4.SetMulticastLoopback(true)                              // IP_MULTICAST_LOOP = 1
u4.SetMulticastTTL(0)                                      // localhost only
u4.JoinGroup(lo, &net.UDPAddr{IP: net.IPv4(239,0,0,1)})    // IP_ADD_MEMBERSHIP on loopback
```
TCP command listener mirrors `_init_command_listen_socket` + `_try_accept` (the **reverse-connect**): listen on the configured `-command-addr` (default `127.0.0.1:6776`), broadcast `open_connection{command_ip,command_port}` **carrying that exact port**, the **editor dials back to whatever port we advertised**, we accept; `6 × 5s`, re-broadcasting each attempt.

**Shared-port rule (load-bearing for A/B and security).** UDP `:6766` is *safely shared* by multiple local clients — it's multicast with `SO_REUSEADDR`, so every joined client receives every `pong`. The TCP command endpoint is **not** shareable: the editor connects back to the single port advertised in `open_connection.command_port`, and two clients advertising the same port both bind it under `SO_REUSEADDR`, so Windows routes the editor's connect-back to *one of them nondeterministically*. **Therefore the TCP command port must be unique per client that is concurrently issuing commands.** Because we advertise whatever `-command-addr` port we bind, giving a second concurrent client a different port (e.g. `:6777`) fully de-conflicts it with **no editor change**. This is exactly why the Phase-4 A/B harness runs the Go server on `:6777` (§15) and why the security posture (§12) treats `:6776` contention as a real, addressable item rather than an assumption.

### 6.4 Concurrency model, cancellation, tainting — with an explicit recovery contract
The channel is single-flight; model `RunCommand(ctx, code, mode)` behind a `sync.Mutex` (== Python `_LOCK`). Apply `ctx` via socket deadlines **and** a `ctx.Done()` watcher that calls `conn.SetDeadline(now)`. **Editor commands are uninterruptible** (the plugin runs Python synchronously on the game thread), so on timeout/cancel we cannot truly abort — we return `ErrTimeout` and **taint** the connection (an unread late `command_result` would desync the stream); a tainted conn is dropped, never reused.

**Timeout-recovery contract (stated, and proven by test).** The taint-and-reconnect design prevents stale-result desync *by construction*: a tainted connection is closed and never read again, so the abandoned command's late `command_result` — whenever the editor finally finishes it on the game thread — is written to a socket we have already closed and discarded. The **next** `Call` opens a *fresh* TCP listen socket, performs a *fresh* reverse-connect, and binds a *fresh* `json.Decoder`, so there is no buffered surplus or half-read frame to misattribute to the new command. Two observable outcomes are contracted:
1. **Editor idle by next call:** reconnect succeeds; the new command returns its own correct result. (fakeeditor test `slow_then_recover`: slow cmd → ctx timeout → taint → editor completes the abandoned cmd late on a closed socket → next `Call` reconnects clean and returns the right result, no desync.)
2. **Editor still busy finishing the abandoned command:** the reverse-connect `accept` exhausts `6 × 5s` and surfaces `ErrConnectionLost` with actionable text (`"editor busy or unreachable; a previous command may still be running on the game thread"`). (fakeeditor test `busy_accept`.)

This makes timeouts during builds/PIE — which are *certain* in unattended runs — a defined, recoverable state rather than a silent corruption.

### 6.5 Discovery goroutines (mirror `_run_broadcast_listen_thread`) + node selection
Two goroutines over a `sync.RWMutex`-guarded `nodeTable`, lifecycle bounded by `context.Context`:
- **recvLoop:** `SetReadDeadline(now+100ms)` (== Python `settimeout(0.1)`, also observes ctx promptly) → `ReadFrom` → filter-in → on `pong` upsert `{data,lastPong}`. The pong `data` carries the editor's advertised metadata (engine version/root, **project name/root**) — retained on the node record.
- **pingLoop:** 1s ticker → broadcast ping + sweep nodes older than 5s.
- **Node selection (new, cheap robustness).** Python's `_connect` grabs `nodes[0]` arbitrarily; on a multi-project dev box that can attach to the *wrong* editor. `WaitForNode` filters discovered nodes by `UMCP_PROJECT_DIR` against the pong's advertised `project_root`/`project_name`, picks the match, and **logs the chosen `node_id`**. If `UMCP_PROJECT_DIR` is unset or no node matches, it falls back to the first discovered node (Python parity) and logs the ambiguity. Multi-editor remains a non-goal; this just prevents a foot-gun.
- **Shutdown:** `cancel()` → `u4.Close()` (unblocks in-flight `ReadFrom`) → `wg.Wait()`. No goroutine leak across reconnects — matters for long sessions.

### 6.6 Reconnect (bridge layer) & long-session resilience
Preserve `unreal_bridge`'s 2-attempt loop: on `ErrConnectionLost`/taint → drop → re-discover (`WaitForNode`, 5s, project-filtered) → re-open → **re-verify/install the companion module** (version sentinel, §8) → retry once. `ErrEditorNotFound` is **not** retried (surfaces the actionable "start the editor" message). Background discovery runs continuously, so an editor restart (new `node_id`) self-heals. **v1 improvement over Python:** keep discovery warm across command-channel reconnects so a reconnect re-does only the fast TCP reverse-connect, not the 5s UDP window.

### 6.7 End-to-end sequence the spike must reproduce
```
Go client                                   UE Editor (PythonScriptPlugin)
  ── ping (UDP 239.0.0.1:6766) ─────────────►
  ◄──────────── pong (source=<node-id>, data{project_root,...}) ──   (every 1s; 5s timeout)
  [pick node by UMCP_PROJECT_DIR; log node_id]
  listen TCP <command-addr>   (default 127.0.0.1:6776)
  ── open_connection {ip, port=<command-addr port>} ─────────────►
  ◄══════════ TCP connect back to that port ══════   (reverse-connect; accept; bind json.Decoder)
  ── command {command, exec_mode, unattended} ═►
  ◄═════════ command_result {success,result,output} ═
  [P1 probe: cmd1 ExecuteFile "_probe=1"; cmd2 EvaluateStatement "globals().get('_probe',0)" → 1]
```

---

## 7. MCP Server Layer — SDK, stdio, image, structured results

**SDK: `github.com/modelcontextprotocol/go-sdk/mcp` (D1).** Alternatives: `mark3labs/mcp-go` (mature, community-governed — the fallback) and `metoro-io/mcp-golang` (thinner). For a long-lived tool whose sole client is Claude Code, tracking the spec-authoritative implementation minimizes drift; generics give free JSON Schema + structured output.

- **stdio:** `server.Run(ctx, &mcp.StdioTransport{})`. The SDK owns stdout (framed JSON-RPC); we never write stdout ourselves (§11).
- **image (`take_screenshot`, `pie_screenshot`):** `&mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: png, MIMEType:"image/png"}}}` — replaces FastMCP `Image(...)`.
- **structured (`editor_status`, `list_actors`, `get_actor`, `spawn_actor`, `list_assets`, `import_assets`, …):** typed `Out` struct → SDK reflects an output schema into `result.StructuredContent` and mirrors a text rendering into `Content`. `marshal.go` adds the `TextContent` explicitly if the pinned version doesn't auto-mirror (Claude Code consumes text content — belt-and-suspenders).
- **cross-cutting:** `server.AddReceivingMiddleware(loggingMW, recoverMW, timeoutMW)` — one place for per-call structured logging, panic recovery (a bad tool call can never kill an unattended session), and default timeout injection. Handlers stay pure. This is the DRY win the Python version lacks.

Registration DRY:
```go
func add[In, Out any](s *mcp.Server, name, desc string, h mcp.ToolHandlerFor[In, Out]) {
    mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc}, h)
}
```
**Error boundary (enforced in `errToResult`):** *tool-execution* failures (no actor, editor said no) → `IsError:true` result with readable text (model-visible → Claude adapts); *server-unusable* faults (transport dead, protocol malformed) → returned `error` → JSON-RPC error.

---

## 8. Python-Snippet Management Strategy (the central design question)

**Decision: a companion in-editor module (`mcp_bridge.py`), delivered by `go:embed`, hot-loaded over the wire, invoked with base64-JSON args — with an on-disk fallback (D2).** See §2/C1 for why this beats both verbatim-embed and inline strings.

### 8.1 Options weighed
- **A. Inline Go strings (status quo ported).** Rejected as primary: every tool re-implements interpolation; the `# mcp\n` guard, quoting, and `Rotator` ordering re-encoded per call; nothing lintable; permanent injection hazard; multi-line templating is *worse* in Go than Python.
- **B. `go:embed` `.py` templates + `text/template`.** Better (real files, lint/golden-testable) but `text/template`'s `{{}}` collides with Python dict/set braces and re-introduces per-tool render/escaping logic.
- **C. Companion dispatch module.** Best for DRY/testability/injection-safety; needs a delivery story. **Chosen.**

### 8.2 The design
`internal/snippets/py/mcp_bridge.py` is a real, ruff-/`py_compile`-checked package with a dispatch table:
```python
_MCP_BRIDGE_VERSION = 7
def _mcp_dispatch(op, b64args):
    import json, base64, traceback
    try:
        args = json.loads(base64.b64decode(b64args)) if b64args else {}
        fn = _OPS[op]
        result = fn(args)
        print("__MCP_JSON__" + json.dumps({"ok": True, "result": result}))
    except Exception as e:
        print("__MCP_JSON__" + json.dumps({"ok": False, "error": str(e), "traceback": traceback.format_exc()}))
_OPS = { "spawn_actor": _op_spawn_actor, "list_actors": _op_list_actors, ... }
```
The `_find_actor` helper, `Rotator(roll,pitch,yaw)` compat, SceneCapture2D export, and the throttle-off CDO trick move here **verbatim** from today's snippets.

**The `__main__`-persistence assumption is proven in P1, not assumed.** The whole hot-load model rests on one unproven premise: that globals set by one `ExecuteFile` command survive into the next command's namespace (today's `server.py` re-sends `FIND_ACTOR_SNIPPET` every call, so it never actually relied on persistence). This is now a **2-command probe pulled forward into the P1 `uspike` spike**, the moment a live socket exists: command 1 = `ExecuteFile` `"_probe = 1"`; command 2 = `EvaluateStatement` `"globals().get('_probe', 0)"` must return `1`. **The default snippet mode is chosen from that result:** persistence proven → default `hotload`; not persistent → default `ondisk` (§ fallback below). `install.go` is thus designed against a *known-viable* mechanism, and Risk #3 is retired at P1 rather than discovered at P2.

**Delivery — `bridge/install.go`, on every fresh command connection:**
1. `EvaluateStatement("globals().get('_MCP_BRIDGE_VERSION', 0)")`.
2. If `≠ snippets.Version()` → `ExecuteFile` the embedded source once (installs `_mcp_dispatch` + ops into the editor's persistent `__main__` namespace). The version **sentinel** makes cold/stale/upgraded sessions self-correct with no editor restart — this is the mechanism that neutralizes Draft A's version-sync fear.

**Invocation — every structured tool is one code path:**
```go
func (b *bridge) Call(ctx context.Context, op string, args any) (json.RawMessage, error) {
    j, _ := json.Marshal(args)
    code := fmt.Sprintf("_mcp_dispatch(%q, %q)", op, base64.StdEncoding.EncodeToString(j))
    res, err := b.uexec.RunCommand(ctx, "# mcp\n"+code, ExecFile) // # mcp guard + marker parse live ONCE
    ...
}
```
**Why base64:** JSON is not valid Python (`true/false/null`); a base64 ASCII literal has **zero** characters needing escaping and **zero** injection surface — the editor does `json.loads(base64.b64decode(arg))`. This is precisely what erases Draft A's `pybool`/`pyfloat`/`_embed`-nil parity landmines, and it means tool *arguments* can never inject Python (a security property, §12). The `# mcp\n` prefix and `__MCP_JSON__` scrape exist in exactly one place.

**DX — adding a tool is 3 edits:** (1) add `_op_x` + register in `_OPS`; (2) add Go `In`/`Out` structs; (3) add one `add(s, "x", desc, x(b))` line. No Go string-building.

**Fallback (config `UMCP_SNIPPET_MODE`):** if the P1 probe shows `__main__` persistence is flaky on 5.7 (or it later regresses), `ondisk` mode writes `mcp_bridge.py` to `<Project>/Intermediate/PyMCP/` (gitignored → no `/Content` pollution) with a bootstrap `sys.path.insert` + import; **same module, on-disk delivery** — designed-in, not a rewrite. **`execute_python`/`execute_console_command` keep the raw `ExecuteFile`/`EvaluateStatement` path** so Claude is never boxed in.

The companion module + its Python-side tests are the **only** Python we still own, now lintable and unit-testable against a stubbed `unreal` module (see §13 Testing).

---

## 9. Full Tool Catalog (parity + e2e additions, grouped, with signatures)

Parity names (the 16) are **frozen**. New tools are flat `group_verb` snake_case. Column *K*: `PROTO`=raw remote-exec, `RPC`=companion dispatch, `SUB`=Go subprocess, `IMG`=image, `JOB`=async job. Go optional args use **pointers** where zero ≠ intended default; a pointer-optional is nil-defaulting (e.g. `z *float` means "when nil, the op uses 100"), never `*float = 100` — a pointer cannot both be optional and carry a non-nil literal default. Where a whole vector is optional it is `*[3]float`.

### Group A — Session & raw exec (parity)
| Tool | Signature | K | Result |
|---|---|---|---|
| `editor_status` | `()` | RPC | struct `{reachable, engine_version, project_dir, current_level, is_in_pie, editor_pid, bridge_version, snippet_mode}` (additive fields; text unchanged) |
| `execute_python` | `(code string, evaluate bool=false)` | PROTO | `evaluate ? Eval : FormatOutput(RunPython)` → text |
| `execute_console_command` | `(command string)` | PROTO | text |

### Group B — Level & actor authoring (parity + additions)
| Tool | Signature | K | Result |
|---|---|---|---|
| `open_level` | `(level_path string)` | RPC | text (saves dirty first) |
| `list_actors` | `(name_filter string="")` | RPC | `[]ActorRef{label,class,location[3]}` |
| `get_actor` | `(actor_label string)` | RPC | struct `{label,class,path,location,rotation_pyr,scale,components[]}` |
| `spawn_actor` | `(class_path string, x,y float=0, z *float (nil→100), pitch,yaw,roll float=0, label,static_mesh_path string="")` | RPC | `ActorRef` |
| `delete_actor` | `(actor_label string)` | RPC | text |
| `set_actor_transform` | `(actor_label string, location,rotation_pyr,scale *[3]float)` | RPC | text (rotation_pyr `[pitch,yaw,roll]`→`Rotator(roll,pitch,yaw)` editor-side) |
| `apply_level_recipe` ✚ | `(script_path string, save bool=true, clean_slate bool=true)` | RPC | `{actors_before,actors_after,missing_meshes[],errors[],saved}` — formalizes the team's idempotent `Scripts/*.py` pattern (clean-slate destroy non-WorldSettings + save) and surfaces `MISSING:` prints |
| `level_snapshot` ✚ | `()` | RPC | `token` |
| `level_diff` ✚ | `(before_token string="")` | RPC | `{added[],removed[],moved[]}` — verify a recipe did what was intended |

### Group C — Assets (parity + additions)
| Tool | Signature | K | Result |
|---|---|---|---|
| `list_assets` | `(path string="/Game", recursive bool=true, limit int=200)` | RPC | `{total,assets[]}` |
| `import_assets` | `(file_paths []string, destination_path string="/Game/Imported", options *{as_skeletal,import_materials,import_textures})` | RPC | `{imported[],failed[]}` (options additive) |
| `asset_reimport` ✚ | `(asset_path string)` | RPC | `{ok}` — source art changed mid-session |
| `asset_info` ✚ | `(asset_path string)` | RPC | `{class,bounds,num_lods,materials[],nanite}` — recipes need mesh bounds (cf. `build_foundry.py` tiling) |
| `create_material_instance` ✚ | `(parent, dest string, params {scalar,vector,texture})` | RPC | `{path}` — recipes consume `/Game/Materials/M_*` |

### Group D — Play / PIE observation (parity + the verify core ✚)
| Tool | Signature | K | Result |
|---|---|---|---|
| `start_play` | `(simulate bool=false)` | RPC | text (additive struct field `log_marker`) |
| `stop_play` | `()` | RPC | text (additive `{warnings,errors}` from log slice) |
| `pie_observe` ✚ | `(actors_of_interest []string=nil)` | RPC | `{gamestate, players[], counts{}, actors[]}` read from the **PIE `Game` world** (`UnrealEditorSubsystem.get_game_world()`), using a **per-class allowlist** of readable fields defined in `mcp_bridge.py` (see below) |
| `pie_wait_until` ✚ | `(predicate string, timeout_s float)` | RPC/JOB | `{met,elapsed_s,final_state}` — whitelisted expr over the (stable, allowlisted) observe schema, polled ~4 Hz; makes PIE deterministic vs `sleep(N)` |
| `pie_exec` ✚ | `(target string, ufunction string, args {}=nil)` | RPC | `{ok,result}` — invoke a `BlueprintCallable` UFUNCTION on a live PIE actor (deterministic driving; see §10 Tier 2 debug library) |

**`pie_observe` mechanism (specified, not hand-waved).** Generic enumeration of "all `BlueprintReadOnly` UPROPERTYs" is not reliable from editor Python (only script-exposed properties are reachable, and `get_editor_property` needs exact names). For v1, `mcp_bridge.py` holds an **explicit per-class allowlist**, e.g. `AAesirGameState → {WaveNumber, EnemiesRemaining, WaveState, MissionType, ...}`, player pawn/controller → `{Health, Gold, Location, ...}`, each read via `get_editor_property` with a `try/except` that **skips unreadable fields gracefully** (never fails the whole observe). Plus a class histogram (`counts`) over all actors in the PIE world (no per-class knowledge needed). This makes `pie_observe` output **stable and predictable** — which is exactly what `pie_wait_until` predicates need to be deterministic. Full generic auto-reflection is deferred (§16); the allowlist is a ~20-line table edited alongside game changes.

### Group E — Visual verification (parity + additions)
| Tool | Signature | K | Result |
|---|---|---|---|
| `take_screenshot` | `(width int=1280, height int=720, camera_location,camera_rotation_pyr *[3]float)` | RPC+IMG | SceneCapture2D PNG (editor-world, backgrounded-safe, auto-frames bounds; **not** during PIE). Per-call filename `mcp_<pid>_<rand>.png` fixes today's fixed-name race |
| `pie_screenshot` ✚ | `(width int=1920, height int=1080)` | RPC+IMG | real backbuffer via `HighResShot`/`AutomationLibrary.take_high_res_screenshot`; **exposure-correct** capture RESUME demands for lighting; only valid during PIE |

### Group F — C++ build orchestration ✚ (the differentiator; SUB)
| Tool | Signature | K | Result |
|---|---|---|---|
| `build_compile` | `(strategy "auto"\|"livecoding"\|"full"="auto")` | SUB/JOB | `{success, strategy_used, diagnostics:[{tool,severity,file,line,column,code,message}], raw_tail, duration_s}` — **async job; streams UBT/UHT/MSVC (and post-launch shader-compile) progress lines via `job_status`** |
| `live_coding_compile` | `()` | PROTO | **strict parity — unchanged.** Fires `LiveCoding.Compile` and returns immediately with the exact current text ("Live Coding compile triggered (see editor log/Live Coding console for status)"). Fire-and-forget; does **not** block, tail, or diagnose. Diffs verbatim in Phase 4. |

**Why `live_coding_compile` stays fire-and-forget (blocker resolution).** Turning it into a blocking, log-tailing, diagnostic-returning tool would change both its timing *and* its text/output shape — which violates the frozen-parity rule and would make its Phase-4 A/B result "diff, but is it a regression?" ambiguous exactly in the build-loop area we care most about. So the blocking hot-reload contract lives entirely in the **new** `build_compile(strategy="livecoding")`, which is *expected* to differ and has its own acceptance criteria (P7). `live_coding_compile` remains the thin, text-identical parity tool.

**`build_compile` strategy classifier** (the most valuable tool): inspect `git diff --name-only` — if a `.h` touched reflection macros (`UCLASS/USTRUCT/UENUM/UPROPERTY/UFUNCTION`) or files were added/removed → **full**; else **livecoding**. Live Coding reliably patches function bodies in existing TUs but *not* new reflected types/files — this classification is the whole game. On livecoding "patch failed / new UObject" → **auto-escalate to full** and tell Claude. **full** teardown → `Build.bat AesirWaveDefenseEditor Win64 Development -Project=<uproject> -WaitMutex`, stream/parse UBT/UHT/MSVC live (progress → `job_status`), relaunch editor + reopen prior map. **livecoding** triggers `LiveCoding.Compile` then Go tails `Saved/Logs/*.log` from a pre-marker offset for `LogLiveCoding` success / `Compile failed` / `error C####`.

**Streamed progress (no more blind windows).** Because a `full` rebuild + relaunch blinds the operator for 60–120s, `build_compile` is a **JOB** that pumps each parsed UBT/UHT/MSVC line — and, after relaunch, editor **shader-compile progress** (`Compiling shaders (N/M)`) — into the job's progress buffer, retrievable via `job_status`. A stuck build is thus distinguishable from a slow one, and an unattended run never *looks* hung.

### Group G — Logs & telemetry ✚ (SUB)
| Tool | Signature | K | Result |
|---|---|---|---|
| `logs_mark` | `()` | SUB | `marker` (byte offset) |
| `logs_tail` | `(lines int=200, min_severity string="Display", categories []string=nil)` | SUB | `{lines[]}` |
| `logs_since` | `(marker string, min_severity string="Display")` | SUB | `{lines[], errors, warnings, ensures}` — attributable output primitive |

**`logs_since` attribution is not purely file-tail-dependent.** UE buffers log writes, so the last few lines of a just-finished command can lag on disk and a naive byte-offset read would miss the tail. Therefore `logs_since` **merges two sources**: (1) the `Saved/Logs/*.log` slice from `marker` to EOF, and (2) the `command_result.output` entries (`Info/Warning/Error`) captured for every remote-exec command the server ran inside the marked window (we already do this for `stop_play`'s warnings/errors). The merge is de-duplicated by `(severity, message)` and ordered by capture time, so an error emitted by the final command is attributed even if it hasn't flushed to the file yet.

### Group H — Source control ✚ (SUB)
| Tool | Signature | K | Result |
|---|---|---|---|
| `git_status` | `()` | SUB | `{branch,staged[],unstaged[],untracked[]}` |
| `git_diff` | `(paths []string=nil)` | SUB | `{diff}` |
| `git_checkpoint` | `(message string, paths []string=nil)` | SUB | `{commit}` — respects `.gitattributes`/LFS; never stages `Saved/`,`Intermediate/`,`DerivedDataCache/`; never bypasses hooks |
| `git_revert_to` | `(ref string)` | SUB | `{ok}` — roll back a bad edit in an unattended run |
| `git_log` | `(limit int=20)` | SUB | `[{hash,subject,date}]` |

### Group I — Editor lifecycle & jobs ✚
| Tool | Signature | K | Result |
|---|---|---|---|
| `project_ensure_open` | `(uproject string="", map string="", timeout_s float=300)` | SUB/JOB | `{launched,editor_pid,ready}` — launch `UnrealEditor.exe <uproject> [map] -nosplash` if no node discovered; idempotent |
| `editor_restart` | `(save bool=true, timeout_s float=300)` | SUB/JOB | `{editor_pid}` — confirmed `save_all` → quit/kill → relaunch → reconnect → reopen map |
| `save_all` | `()` | RPC | text (parity) |
| `job_status` | `(job_id string)` | — | `{status, progress_lines[], result?}` — poll a long op (incl. streamed build/shader progress) without blocking the channel |
| `job_cancel` | `(job_id string)` | — | cancel |

**Lifecycle timeouts sized for reality (GameDev fix).** A cold `aesir-wave-defense` editor after a *full* rebuild routinely exceeds 60s before it answers pings — first-open shader compilation and asset-registry scan dominate. So `project_ensure_open`/`editor_restart` do **not** hard-fail at a fixed 60s; they **wait for first pong** with a generous default ceiling of **300s** (configurable), and the underlying job **logs discovery progress** every few seconds (`"waiting for editor pong… Ns"`, then streamed shader-compile progress once connected) so an unattended run never false-fails a relaunch and a genuinely hung launch is visible via `job_status`.

On connect, the server auto-applies the **throttle-off CDO trick** (`Default__EditorPerformanceSettings.bThrottleCPUWhenNotForeground=False`) so backgrounded builds/PIE/screens don't stall.

**Async jobs:** `build_compile`, `pie_wait_until`, `editor_restart`, `project_ensure_open` return `{job_id,status}`. Jobs run in the background and touch the single-flight channel only in short bursts (a `pie_wait_until` poll acquires/releases the mutex per tick; `build_compile` is mostly a subprocess), so they never monopolize it. `job_status`/`job_cancel` poll/cancel.

---

## 10. The Build / Play / Verify E2E Loop

The loop Claude drives — each node is one tool returning structured JSON so Claude branches; nothing is hardcoded in Go:

```
edit .cpp/.h  (Claude, file tools)
   │
build_compile(strategy=auto)   → job; poll job_status for streamed UBT/UHT/MSVC + shader progress
   ├─ diagnostics=[] ─────────────► continue
   └─ diagnostics=[{file,line,code,msg}] ─► Claude fixes ─► retry
   ▼
project_ensure_open(map="/Game/Maps/L_Hub", timeout_s=300)   (waits for first pong; streams shader progress)
   ▼
apply_level_recipe("Scripts/build_foundry.py")  [if authoring] → missing_meshes/errors
   ▼
take_screenshot(...)                 (fast geometry sanity; backgrounded-safe)
   ▼
open_level("/Game/Maps/L_Arena")
   ▼
m = logs_mark(); start_play()
   ▼
pie_wait_until("gamestate.WaveState == 'InProgress'", 20)
pie_exec("gamestate","ForceStartWave",{n:5}); pie_wait_until("counts.EnemyCharacter_Serath >= 1", 20)
   ▼
pie_observe()   → {gamestate, players[], counts, actors}   (allowlisted, stable schema)
pie_screenshot() → exposure-correct PNG (lighting/skin confirm)
logs_since(m, "Warning") → ensures/errors attributable to this run (file-tail ∪ captured command output)
   ▼
VERIFY: Claude asserts observed == expected across state+visual+log
   ▼
stop_play()
   ▼
git_checkpoint("feat: boss spawns on wave 5")   [on success]
```

**PIE driving — three tiers, honest about feasibility:**
- **Tier 1 (v1, feasible now):** read `AAesirGameState`/player state out of the PIE `Game` world (`UnrealEditorSubsystem.get_game_world()`) via the `pie_observe` **allowlist**, assert. Zero per-game Go code; degrades gracefully on unreadable props; stable enough to be a predicate substrate.
- **Tier 2 (v1, needs a small game-side hook):** synthetic keypress is unreliable headless; the culture-fitting move (C++-only, no gameplay BP) is a thin `UAesirDebugFunctionLibrary` of `BlueprintCallable` statics (`ForceStartWave`, `GiveGold`, `SpawnEnemyAt`, `SetWaveState`, `DamagePlayer`, `TeleportPlayer`) that `pie_exec` calls. ~1 header/cpp; **recommended, not a blocker** — Tier 1 works without it.
- **Tier 3 (later):** Gauntlet/`AutomationDriver` synthetic input, functional-test actors, replay. Deferred (§16).

**Load-bearing check made explicit (GameDev fix).** The entire verify loop assumes remote-exec commands are serviced **on the game thread during active PIE**, so `pie_observe`/`pie_exec` see live PIE-world state (not a stale editor world, and not merely a Simulate-in-Editor world). This is very likely true but is the linchpin, so it gets its **own named P8 smoke check** rather than riding on the "drive a wave to WaveNumber≥2" criterion (which could pass via SIE): mutate PIE state through `pie_exec` and confirm the *same* mutation is visible in the next `pie_observe` while PIE is running (§16, P8).

---

## 11. Config, Logging & Error Handling

**Config (`internal/config`) — flags > env > defaults; stdlib `flag` + a small env fallback (no cobra/viper):**

| Env | Flag | Default | Purpose |
|---|---|---|---|
| `UMCP_MULTICAST_GROUP` | `-group` | `239.0.0.1:6766` | discovery (shared-safe via multicast+REUSEADDR) |
| `UMCP_BIND_ADDR` | `-bind` | `127.0.0.1` | multicast bind/IF |
| `UMCP_COMMAND_ADDR` | `-command-addr` | `127.0.0.1:6776` | TCP reverse-connect listener. **Must be unique per concurrently-command-issuing client** — overridden to `:6777` for the Phase-4 A/B run (§15). |
| `UMCP_DISCOVERY_TIMEOUT` | `-discovery-timeout` | `5s` | WaitForNode |
| `UMCP_COMMAND_TIMEOUT` | `-command-timeout` | `120s` | default per-call (screenshot/build override up; `0`=block-forever parity) |
| `UMCP_PROJECT_DIR` | `-project` | (req. for screenshot/build/logs/git; also filters node discovery) | fs readback, Build.bat, log/git roots, **node-by-project selection** |
| `UMCP_ENGINE_DIR` | `-engine` | `D:/Unreal/Engine/UE_5.7` | Build.bat/RunUAT, editor launch |
| `UMCP_SNIPPET_MODE` | `-snippet-mode` | `hotload`* | `hotload`\|`ondisk` (*default decided by the P1 persistence probe; §8.2) |
| `UMCP_AUTO_RELAUNCH` | `-auto-relaunch` | `false` | relaunch editor on crash (unattended policy) |
| `UMCP_LOG_LEVEL`/`_FORMAT` | `-log-level`/`-log-format` | `info`/`json` | slog |

**Logging (`internal/logging`) — stdout purity is non-negotiable.** stdout carries the MCP JSON-RPC frame; one stray byte corrupts the session. Therefore: (a) slog handler on `os.Stderr`; (b) `log.SetOutput(os.Stderr)` to cage the stdlib default logger; (c) **never** `fmt.Print*` to stdout — enforced by a grep gate in `build.ps1`/CI (`fmt.Print(`/`os.Stdout` outside the SDK path fails the build). Note the two distinct "stdouts": our process stdout (protocol, sacred) vs the editor snippet's `print("__MCP_JSON__"…)` (captured into `command_result.output`, entirely separate). Editor `Warning`/`Error` entries log at debug. Startup logs version/commit/config summary and the selected `node_id`.

**Error taxonomy (`internal/mcperr`):**
```go
var (
  ErrEditorNotFound = errors.New("no Unreal Editor node discovered")        // → IsError text: "start the editor with the project open"
  ErrConnectionLost = errors.New("command connection lost")                  // → reconnect-once; if final (incl. busy-accept exhaustion), JSON-RPC error with actionable text
  ErrCommandFailed  = errors.New("editor reported command failure")          // carries traceback/output; → IsError text
  ErrProtocol       = errors.New("malformed remote-exec message")            // → JSON-RPC error
  ErrTimeout        = errors.New("editor command timed out")                 // wraps DeadlineExceeded; taints conn (§6.4 recovery contract)
  ErrBridgeVersion  = errors.New("companion module install/verify failed")   // → JSON-RPC error
)
type CommandError struct{ Op string; Output []OutputEntry; Result string }   // Unwrap → ErrCommandFailed
```

---

## 12. Security & Trust Model

The server's entire purpose is to execute arbitrary Python in the editor, unattended, for long sessions. That is a code-execution channel, so its trust model must be *written and analyzed*, not implied by "it binds loopback." Each item below is explicitly mitigated or knowingly accepted.

**12.1 Operating context / threat model.** Single-user, trusted, local Windows 11 developer workstation. The MCP client (Claude Code) runs locally, as the same user, and is trusted — it is already granted file and shell tools on this machine. There is no multi-tenant, no remote client, and no untrusted network in scope. The trust boundary is therefore **"any process running as this user on this host"** — identical to the boundary of the file/shell tools Claude already uses, and identical to the existing Python server and Epic's own plugin.

**12.2 Network exposure = zero (by construction).** Every socket is loopback-only. UDP discovery binds `127.0.0.1:6766` and uses multicast group `239.0.0.1` with **TTL 0**, so datagrams are never forwarded off the host. The TCP command channel listens on `127.0.0.1:6776` (or the configured `-command-addr`) and the editor dials back over loopback. No socket binds a routable interface; nothing is reachable from the LAN or internet. This is the *same* posture Epic's PythonScriptPlugin remote-execution opens when the user enables it — the Go port adds **no** network attack surface beyond what the editor plugin already exposes.

**12.3 Arbitrary-Python execution is inherent and accepted-by-design.** The PythonScriptPlugin is the editor's *only* scripting surface, so any client that can speak this protocol can run arbitrary Python in the editor. This is equally true of the current Python `server.py`, of Epic's plugin, and of the Go port — the rewrite neither increases nor decreases it. We **explicitly accept** it: it is not a regression, and it is bounded by the same "processes running as this user" boundary as the rest of the toolchain. `execute_python`/`execute_console_command` are the one *intended* arbitrary-code surface; every *other* tool ships **data** (base64-JSON args the editor `json.loads`es — §8.2), so tool arguments cannot inject Python. Arbitrary execution is a single, named, front-door capability, not an ambient property of every tool.

**12.4 Reverse-connect posture on `:6776` / Windows `SO_REUSEADDR` (the one real local-attacker vector).** Windows `SO_REUSEADDR` lets *another local process owned by the same user* also bind `:6776`, so there are two concrete failure modes, both addressed:
- **(a) Connect-back theft (DoS).** A rogue local listener on `:6776` could receive the editor's connect-back instead of us. Consequence: our `accept` never completes → `6×5s` exhaustion → `ErrConnectionLost` with actionable text (§6.4). This is a denial-of-service, **not** a data-integrity breach — we never treat a missing connection as success.
- **(b) Result spoofing.** A rogue process could instead connect *to our* listener and feed fake `command_result`s. We defend at the message layer: we require the loopback peer, and we **validate every accepted result** — `magic=="ue_py"`, `version==1`, `passesFilter` (i.e. `source == the node_id we targeted in the` `open_connection`, and `dest == our per-session UUID`). Our source UUID is generated per session with `crypto/rand` and only ever traverses loopback; the target `node_id` is the discovered editor's id. A spoofer would have to know both, which requires already reading our loopback traffic — i.e. already being a same-user local process, which is inside the accepted boundary. So a hijack degrades to a *failed/again-tainted* command, never a silent wrong-result.
- **Hardening option (documented, on-by-config).** Because we advertise whatever port we bind in `open_connection.command_port`, the code supports **any** command port, including an **ephemeral OS-assigned port per command** — which sidesteps `SO_REUSEADDR` contention entirely (each listener uses a fresh, unique port no other process is squatting). We default to the fixed `:6776` for parity/debuggability, but if local-port contention is ever observed, flipping to ephemeral-per-command is a config change, not a redesign. (This is the same mechanism that de-conflicts the Phase-4 A/B run — §6.3/§15.)
- **Residual risk: accepted.** On a single-user dev box, residual same-user local-process contention on `:6766`/`:6776` is accepted as *equivalent to the existing plugin's exposure* (Epic's client has the identical `SO_REUSEADDR` behavior). Tracked as Risk #15 (§17).

**12.5 Filesystem & secrets.** The server reads `UMCP_PROJECT_DIR` for screenshots/logs/git and shells `Build.bat`/`git` under the user's ambient credentials. It handles no secrets of its own. `git_checkpoint` never bypasses hooks and never stages `Saved/`, `Intermediate/`, or `DerivedDataCache/`, so it can't accidentally commit machine-local or generated content. Screenshot/log reads are confined to the project tree.

**12.6 Summary of decisions.** Loopback + TTL 0 → no network exposure (mitigated by design). Arbitrary Python → inherent to the channel, accepted, and narrowed to one front-door tool by base64-data dispatch. `:6776` same-user contention → mitigated by result validation and a documented ephemeral-port hardening switch, residual accepted and risk-registered.

---

## 13. Testing Strategy

**`testdata/fakeeditor` — the differentiator (D6).** A full editor-side protocol implementation in-repo: joins the group, answers `ping`→`pong` (advertises node_id + fake engine/**project** metadata so node-by-project selection is testable), and on `open_connection` dials back the advertised endpoint (reverse-connect), reads `command`, and replies with **scriptable** `command_result`: echo, canned `__MCP_JSON__`, `success:false`+traceback, slow (timeout/recovery tests), `>64 KiB` and exact-multiple-of-8192 (framing tests), malformed/wrong-magic/wrong-source (filter/spoof tests). ~90% of tests need no live UE.

**Seams (`uexec/seams.go`):** discovery/command accept injected `net.PacketConn`/`net.Listener`/`clock` interfaces, so unit tests use ephemeral ports / unicast loopback and never depend on real multicast (flaky in CI). True-multicast tests run behind `//go:build integration` on Windows only.

**Coverage:**
- `uexec`: discovery find/age-out; **node-by-project selection** (2 nodes, pick by project, log id; fallback + ambiguity log); reverse-connect handshake incl. 6× retry; framing of large & segmented & exact-8192-multiple results; magic/version/self-source filtering; **timeout→taint→reconnect recovery contract** — `slow_then_recover` (late result on a closed socket, next Call reconnects clean, no desync) and `busy_accept` (accept exhaustion → `ErrConnectionLost` with actionable text) per §6.4.
- `bridge`: `__MCP_JSON__` marker parse (mid-line, missing, Warning/Error entries); `FormatOutput` golden tests (prefixes, result-suppression set, `[Execution failed]`, `(no output)`); `Eval` repr; **companion install happens once, re-execs on version bump**; base64 round-trip; reconnect-once; **`logs_since` merge** (file-tail ∪ captured command output, de-duplicated).
- `tools`: `bridge.Bridge` mocked → assert each tool emits the right `op`+args and maps result/image/error correctly; `pie_wait_until` predicate grammar accepts whitelisted comparisons and rejects everything else.
- `snippets`: CI runs `ruff` + `python -m py_compile` (editor's Python 3.11) over `py/*.py`; optional pyright vs `ue-stubs`; the companion package (incl. the `pie_observe` allowlist) has pytest tests against a stubbed `unreal`.
- `go test -race ./...` everywhere; a concurrency test hammers `Call` under the single-flight mutex.

**Live smoke — `cmd/uspike` (the go/no-go gate).** Starts discovery, prints discovered nodes and the **project-selected** `node_id`, opens the command channel, `EvaluateStatement("unreal.SystemLibrary.get_engine_version()")`, prints the result — and runs the **`__main__`-persistence probe** (cmd1 `ExecuteFile "_probe=1"`; cmd2 `EvaluateStatement "globals().get('_probe',0)"` must be `1`), printing the chosen default snippet mode. This exercises the **entire wire protocol against the live editor before any MCP code exists** — if Windows loopback multicast misbehaves in Go, or `__main__` doesn't persist, we learn day one, cheaply, and pick `ondisk` accordingly. **`-selftest` flag** on the production exe (connect + `editor_status` round-trip, non-zero on failure) doubles as an unattended health check. **Wire-diff:** run the Python client and `uspike` side by side, capture the `ping`/`open_connection`/`command` messages, and compare **semantically** (re-parse to canonical form; §6.1) — not raw bytes, since Go/Python JSON whitespace legitimately differs.

**CI (`.github/workflows/ci.yml`):** lint/test (ubuntu: `gofmt -l` gate, `go vet`, `staticcheck`, `golangci-lint`, `go test -race`); snippet job (`ruff` + `py_compile` 3.11); build (windows: static exe, `-version` smoke, `//go:build integration` multicast tests vs `fakeeditor`, upload artifact); release on tag (`goreleaser`/`build.ps1` → exe + SHA256SUMS).

---

## 14. Toolchain Bootstrap (Go is NOT installed)

`scripts/bootstrap.ps1` — user-local, no admin, checksum-verified, session-scoped PATH:
1. If `go version` present and ≥ pinned → skip.
2. Else fetch pinned `https://go.dev/dl/go<VER>.windows-amd64.zip`; **verify SHA256** against `https://go.dev/dl/?mode=json`. Pin `<VER>` to current stable (verify it meets the SDK's `go` directive).
3. Extract to `$env:LOCALAPPDATA\go-sdk\go<VER>`; set `GOROOT` + prepend `GOROOT\bin` to PATH **for the build shell only**.
4. `go env -w GOTOOLCHAIN=local` (deterministic); `go mod download` (or verify `go mod vendor` — recommended for a personal tool that must always build offline).
5. Hand off to `build.ps1`:
```powershell
$env:CGO_ENABLED=0; $env:GOOS='windows'; $env:GOARCH='amd64'
go build -trimpath `
  -ldflags "-s -w -X main.version=$(git describe --tags --always) -X main.commit=$(git rev-parse --short HEAD)" `
  -o dist/unreal-mcp.exe ./cmd/unreal-mcp
```
Pure-Go, no cgo → fully static ~8–15 MB exe, no libc/runtime, no `.venv`. `go mod init github.com/jdziat/unreal-mcp-server`; commit `go.sum`.

---

## 15. Migration & Cutover Plan (never lose a working server)

The old Python files stay on disk **untouched** the entire time → rollback needs no rebuild.

**The A/B TCP-port rule (blocker resolution).** Both the Python client and the Go client perform the editor's **reverse-connect** on a TCP command listener that *defaults to `127.0.0.1:6776`*. UDP discovery on `:6766` is safely shared (multicast + `SO_REUSEADDR` delivers pongs to both), **but the TCP command endpoint cannot be shared**: the editor connects back to whatever single port a client advertises in `open_connection.command_port`, and two clients on the same port both bind it under `SO_REUSEADDR`, so Windows routes the connect-back nondeterministically (§6.3). Running both servers with the default `:6776` would make the A/B diff — the very artifact that authorizes cutover — flaky on day one. **Fix:** during A/B, the Go server gets a *distinct* command port via `-command-addr 127.0.0.1:6777`. Because we advertise the port we bind, the editor dials back to `:6777` for Go and `:6776` for Python with **no editor change**, and the two command channels never contend.

- **Phase 4 — Side-by-side A/B.** Register both servers in `aesir-wave-defense/.mcp.json`, with the Go server pinned to a non-6776 command port:
```json
{ "mcpServers": {
  "unreal":    { "command": ".venv/Scripts/python.exe", "args": ["server.py"] },
  "unreal-go": { "command": "C:/Users/jorda/code/games/unreal-mcp-server/dist/unreal-mcp.exe",
                 "args": ["-command-addr", "127.0.0.1:6777"],
                 "env": { "UMCP_PROJECT_DIR": "C:/Users/jorda/code/games/aesir-wave-defense",
                          "UMCP_ENGINE_DIR": "D:/Unreal/Engine/UE_5.7" } } } }
```
A scripted parity suite invokes every one of the 16 tools on both servers and diffs outputs (`take_screenshot`/`pie_screenshot` asserted as valid non-empty PNGs; `live_coding_compile` asserted **text-identical**). Python stays the default the user relies on until the diff is green.
- **Phase 5 — Cutover.** Point `mcpServers.unreal.command`/`args` at the exe; keep the Python entry as `unreal-py` for A/B and **one-line instant rollback** (revert that single block). The Go server may keep `-command-addr 127.0.0.1:6777` while `unreal-py` remains registered — a *dormant* Python server binds `:6776` **only** lazily when it actually runs a command (§4), which rollback-only use never does, so keeping distinct ports is a belt-and-suspenders choice. Once the Python entry is deleted from `.mcp.json` (Python fully retired), drop the `-command-addr` override so the Go server reverts to the default `:6776`. **Rule of thumb:** unique TCP command port whenever a second command-issuing client is registered; default `:6776` once Go is the sole client.
- **Rollback** at any later point = re-point `mcpServers.unreal` back to `python.exe server.py`. Zero rebuild, always available.

---

## 16. Phasing & Milestones with Acceptance Criteria

| Phase | Deliverable | Acceptance criteria | Python status |
|---|---|---|---|
| **P0 Bootstrap** | `bootstrap.ps1` installs Go; `go mod init`; empty binary prints `-version` | `go version` ≥ pin; exe runs | untouched |
| **P1 Protocol spike** ★gate | `uexec` + `uspike` + `fakeeditor` | `uspike` returns the engine version **from the live editor**; **`__main__`-persistence probe passes (or selects `ondisk`)**; **node-by-project selection logs the right node_id**; fakeeditor unit tests green (`-race`) incl. exact-8192 framing and timeout→taint→recover; **Windows loopback multicast validated** | untouched |
| **P2 Bridge** | `bridge` + companion `mcp_bridge.py` hot-load + version sentinel + base64 `Call` (built against the P1-proven snippet mode) | `install` re-execs only on version bump; `FormatOutput`/marker/`Eval` golden tests green; reconnect-once + `logs_since`-merge tests green | untouched |
| **P3 Tools (parity)** | all 16 tools; `take_screenshot` ImageContent; static exe | each tool green vs `fakeeditor`; `live_coding_compile` returns today's exact text; `-selftest` passes vs live editor | untouched |
| **P4 A/B parity** | side-by-side `.mcp.json` (Go on `-command-addr :6777`) + diff harness | all 16 tools diff-equal (or intentionally-additive-with-unchanged-text) vs Python; `live_coding_compile` **text-identical**; screenshots valid PNG; **no `:6776` contention (distinct ports verified)** | **default** |
| **P5 Cutover** ★ship-parity | `.mcp.json` points at exe; Python kept as `unreal-py`; port rule per §15 | a real `aesir-wave-defense` session runs end-to-end on Go; rollback verified | rollback only |
| **P6 Reliability** | reconnect/taint/timeout-recovery, warm discovery across reconnects, middleware (log/recover/timeout), signal shutdown, crash detect + `AUTO_RELAUNCH`, throttle-off | `-race` clean; kill+restart editor mid-session → self-heals; timeout mid-command → next call reconnects clean (no desync); panic in a tool doesn't kill the server | shipped |
| **P7 Build orch** | `internal/build` + `build_compile`(auto/livecoding/full) + diagnostics + **streamed progress via `job_status`**; `editor_restart`/`project_ensure_open` (wait-for-pong, 300s ceiling) | inject a deliberate `error C####` → structured diagnostic returned; reflection-macro `.h` change → classified `full`; full rebuild closes/reopens editor and reopens map; **`job_status` streams UBT/UHT/MSVC then shader-compile progress during a full rebuild** (no blind window); `live_coding_compile` still fire-and-forget while `build_compile(strategy=livecoding)` blocks + diagnoses | shipped |
| **P8 PIE + logs** | `pie_observe`(allowlist)/`wait_until`/`exec`/`screenshot`, `logs_mark/tail/since`, `start_play` marker | drive a wave to `WaveNumber>=2` via predicate (no sleep); **named game-thread smoke check: mutate PIE state via `pie_exec` and observe the same change in the next `pie_observe` while PIE is active — proving live-PIE-world servicing on the game thread, not SIE**; `pie_screenshot` is a valid backbuffer PNG; `logs_since` attributes a known warning **even before it flushes to file** (via captured command output) | shipped |
| **P9 Authoring + git** | `apply_level_recipe/level_snapshot/level_diff`, `asset_reimport/info/create_material_instance`, git suite | re-run a recipe twice → identical `level_diff` (idempotent); `git_checkpoint` commits without staging `Saved/Intermediate` and respects LFS | shipped (**= v1.0**) |

★ = risk gate / ship point. P1 is the whole-port go/no-go (also proving `__main__` persistence and node selection); P5 is ship-parity; P9 completes v1.0.

**Deferred (v1.1+), with justification:** C++ scaffolding generator (Claude hand-writes `.h/.cpp` today; `build_compile` verifies — additive later); Blueprint graph authoring (game is C++-only; Python can't reliably author graphs — scope to read-only inspect/set-defaults if ever needed); synthetic-input/Gauntlet (Tier 1/2 cover verification; heavy, input-bug-only); **full generic `pie_observe` property auto-reflection** (v1 allowlist is stable and sufficient; generic reflection is only worth it once many classes need coverage); perf/CSV profiling (not on the idea→verified critical path); navmesh/lighting bake (levels use Lumen + movable lights — low urgency); multi-project/editor (single project today; node-by-project filter already lands the cheap 80%).

---

## 17. Risk Register

| # | Risk | Sev | Mitigation |
|---|---|---|---|
| 1 | **Windows loopback multicast behaves differently in Go than CPython** | High | Copy Python's exact bind + `IP_MULTICAST_IF(loopback)` + `JoinGroup(loopback)` + TTL0 sequence via `x/net/ipv4`; **P1 spike validates against the live editor before any other work**; fallback: enumerate/try all interfaces or bind `0.0.0.0` if the loopback pseudo-iface misbehaves. |
| 2 | **`SO_REUSEADDR` before bind missed** → bind fails on :6766/:6776 (editor holds the port) | Med | `ListenConfig.Control` sets it pre-bind (§6.3); covered by the spike. |
| 3 | **`__main__` namespace persistence assumption false on 5.7** | Med | **Retired at P1**: 2-command persistence probe in `uspike` decides `hotload` vs `ondisk` *before* `install.go` is built (§8.2); version-sentinel re-exec self-corrects cold sessions; `UMCP_SNIPPET_MODE=ondisk` ships the module to `Intermediate/PyMCP/` as the proven fallback. |
| 4 | **TCP result framing (exact-8192 hang / large-payload corruption)** | Med | Persistent `json.Decoder` (§6.2), self-delimiting; explicit framing tests in `fakeeditor`. |
| 5 | **Editor commands uninterruptible → ctx timeout can't truly abort** | Med | Taint-and-reconnect keeps the stream consistent *by construction*; explicit recovery contract (§6.4) proven by `slow_then_recover`/`busy_accept`; generous per-tool timeouts. |
| 6 | **Official SDK API churn (pre-1.0)** | Med | Pin exact version; isolate SDK behind `tools/` + `marshal.go`; `mark3labs/mcp-go` fallback; verify identifiers at impl time. |
| 7 | **Live Coding can't add reflected types/new files** | Med | `build_compile` classifier routes reflection/header/new-file changes to `full`; auto-escalate on livecoding "patch failed"; tell Claude. |
| 8 | **Full rebuild needs editor closed; save must not lose work** | Med | Lifecycle manager does confirmed `save_all` (RPC success) *before* quit/kill; reopen prior map after relaunch; wait-for-pong with 300s ceiling so slow first-open isn't a false-fail. |
| 9 | **stdout contamination corrupts MCP channel** | Med | slog→stderr, `log.SetOutput(stderr)`, no `fmt.Print` to stdout; CI/build grep gate. |
| 10 | **Content-shape parity vs FastMCP** (does Claude see equivalent text/structured content) | Med | De-risked by the P4 A/B **semantic** diff suite (§6.1), not assumed; `marshal.go` mirrors text content. |
| 11 | **Build diagnostics misclassify known-noisy content-BP compile spam as C++ failure** (RESUME: Serath/EasySave BPs) | Low | Classify diagnostics by tool/source; allowlist known-noisy warnings. |
| 12 | **`HighResShot` is async / increments filenames; screenshot fs race** | Low | Pass explicit per-call `mcp_<pid>_<rand>.png`; poll that exact path with a ctx deadline (same pattern as today's 10s poll). |
| 13 | **LFS / staging hygiene in `git_checkpoint`** | Low | Never bypass hooks; never stage `Saved/Intermediate/DerivedDataCache`; let LFS filters run on `.uasset/.umap`. |
| 14 | **Go absent / build reproducibility** | Low | Checksum-verified `bootstrap.ps1`, `GOTOOLCHAIN=local`, committed `go.sum` (+ optional vendor). |
| 15 | **Same-user local-process contention/hijack on :6766 (UDP) / :6776 (TCP reverse-connect)** | Low | Accepted trust boundary is same-user local (§12). UDP `:6766` shared safely (multicast+REUSEADDR). TCP `:6776`: connect-back theft degrades to `ErrConnectionLost` (DoS, never a false success); result spoofing blocked by `magic/version/source(node_id)/dest(session-UUID)` validation on every accepted `command_result`; **ephemeral-per-command port is a config-only hardening switch** if contention is ever observed. Residual accepted as equivalent to the existing plugin's exposure. |
| 16 | **Attaching to the wrong editor on a multi-project box** | Low | Node discovery filters by `UMCP_PROJECT_DIR` against pong project metadata and logs the chosen `node_id` (§6.5); falls back to first node with a logged ambiguity warning. |

---

## 18. Open Questions (non-blocking; resolve during implementation)

1. **`UAesirDebugFunctionLibrary`** — commit the debug hook into the game (unlocks Tier-2 deterministic PIE driving) or stay reflection/console-only in v1? *Recommendation: commit it — small, culture-fitting (C++-only), documents the testable API. Tier 1 ships regardless.*
2. **`build_compile` on ambiguous diffs** — default `full` (safe/slow) or `livecoding`-then-escalate (fast/risky)? *Recommendation: livecoding-then-escalate; escalate automatically on patch failure.*
3. **Auto-relaunch on crash** — opt-in flag (default off) vs default-on for unattended sessions? *Recommendation: off by default, `UMCP_AUTO_RELAUNCH=true` for unattended runs.*
4. **On-disk fallback location** — `Intermediate/PyMCP/` (gitignored, no `/Content` pollution, needs `sys.path.insert`) vs a proper plugin `Content/Python/init_unreal.py` (survives editor restart without Go re-push, but adds a plugin). *Recommendation: `Intermediate/PyMCP/` for cleanliness; hot-load is primary anyway.*
5. **Exact SDK version pin** — verify the current `modelcontextprotocol/go-sdk` release and its `go` directive at implementation time; confirm auto-mirroring of structured→text content (else `marshal.go` adds it).
6. **`pie_wait_until` predicate language** — whitelisted attribute/comparison grammar over the (allowlisted) observe schema (safe) vs a sandboxed Python eval (flexible, riskier)? *Recommendation: whitelisted grammar in v1 — cheap given the allowlist already makes the schema finite and stable.*
7. **`pie_observe` allowlist maintenance** — hand-maintained per-class table in `mcp_bridge.py` (v1) vs deriving it from a game-side `UPROPERTY(meta=(MCPObserve))` tag scan later. *Recommendation: hand-maintained table in v1 (few classes); revisit tag-driven derivation if coverage grows.*
8. **Command-port hardening default** — ship fixed `:6776` (parity/debuggability) or ephemeral-per-command (contention-proof) by default? *Recommendation: fixed `:6776` default with ephemeral available via `-command-addr :0`; flip only if same-user contention is observed (§12.4).*

---

## Appendix: Review Trajectory

- Round 1: GameDev **A-** (2 blocking) · CTO **A** (1 blocking)
- Round 2: GameDev **A+** (0 blocking) · CTO **A+** (0 blocking)

FINAL GRADES — GameDev: A+, CTO: A+.