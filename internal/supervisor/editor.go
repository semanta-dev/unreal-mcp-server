// Package supervisor owns editor processes: the liveness/lease state machine
// (Pool), process spawning and kills, crash relaunch, orphan cleanup, and the pure
// boot-reconciliation logic. Both server topologies build on it; the daemon adds
// session routing on top (package daemon).
package supervisor

import "context"

// Editor is a per-instance command bridge (a bridge.Bridge in production).
type Editor interface {
	Close() error
}

// Spawner brings up and tears down editors. Spawn MUST write the write-ahead intent
// (token) before launching, wait until the editor is ACCEPTING, and return a ready
// bridge + its pid + a stable process identity. Kill force-kills and confirms death.
type Spawner interface {
	Spawn(ctx context.Context, project, token string) (ed Editor, pid int, identity string, err error)
	Kill(pid int) error
}
