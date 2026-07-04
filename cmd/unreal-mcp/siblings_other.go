//go:build !windows

package main

import "log/slog"

// killOrphanSiblings is a no-op off Windows (the editor + wedge case are Windows-only).
func killOrphanSiblings(_ *slog.Logger) {}
