//go:build !windows

package supervisor

import "log/slog"

// KillOrphanSiblings is a no-op off Windows (the editor + wedge case are Windows-only).
func KillOrphanSiblings(_ *slog.Logger) {}
