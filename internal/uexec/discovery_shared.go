package uexec

import (
	"context"
	"log/slog"
	"time"
)

// Discovery is the SHARED discovery layer for the Model-A daemon
// (MULTI_PROJECT_SYSTEM.md §2): ONE multicast socket + ONE node table + ONE server
// self-id, observed by N per-instance Sessions. Editors on different projects are
// disambiguated by their advertised project_root in the shared node table, so a
// single discovery feed fans out to every leased editor — instead of N Sessions
// each opening their own multicast socket (wasteful + collision-prone).
//
// Each per-instance Session (NewOnDiscovery) then owns only its command channel:
// its own ephemeral 127.0.0.1:0 reverse-connect port and its own single-flight
// mutex, pinned to one node id. The daemon owns the Discovery's lifetime; a
// Session sharing it does not close it.
type Discovery struct {
	bc   *broadcastConn
	self string
}

// OpenDiscovery starts the shared multicast discovery (one socket, one node table).
func OpenDiscovery(ctx context.Context, cfg Config, logger *slog.Logger) (*Discovery, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	self := newUUID()
	bc, err := openBroadcast(ctx, cfg.withDefaults(), self, logger)
	if err != nil {
		return nil, err
	}
	return &Discovery{bc: bc, self: self}, nil
}

// SelfID is the shared server node id (the protocol "source" all Sessions use).
func (d *Discovery) SelfID() string { return d.self }

// Nodes snapshots the shared node table.
func (d *Discovery) Nodes() []*Node { return d.bc.nodes.list() }

// WaitForNode blocks until a node matching projectDir is discovered (or timeout).
// This is how a daemon pins a lease to the editor for a specific project.
func (d *Discovery) WaitForNode(ctx context.Context, projectDir string, timeout time.Duration) (*Node, error) {
	// Shared-discovery, project-scoped wait: strict when a project is named (never
	// bind a foreign tenant's node); permissive only for an unscoped probe.
	return d.bc.waitForNode(ctx, projectDir, timeout, projectDir != "")
}

// Close tears down the shared discovery. Only the daemon that opened it calls this;
// Sessions sharing it never do.
func (d *Discovery) Close() error {
	d.bc.close()
	return nil
}
