// Package e2e holds the in-process end-to-end (T1) suite: a real MCP client over
// in-memory transports drives the full server (tools → bridge → real uexec) against
// the wire-level fake editor with the bridgetest op emulator. See
// docs/OVERHAUL_PLAN.md §3.
package e2e
