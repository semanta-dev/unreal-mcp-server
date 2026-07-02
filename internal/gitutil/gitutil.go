// Package gitutil runs git commands in a repository directory. Used by build
// strategy classification (P7) and the git tools (P9). Never bypasses hooks.
package gitutil

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// ErrNoGit indicates the git executable was not found on PATH.
var ErrNoGit = errors.New("git not found on PATH")

// Available reports whether git is on PATH.
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// Run executes `git -C dir <args...>` and returns trimmed stdout. On failure it
// returns an error including stderr.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	if !Available() {
		return "", ErrNoGit
	}
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimRight(stdout.String(), "\n"), errors.New("git " + strings.Join(args, " ") + ": " + msg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// IsRepo reports whether dir is inside a git work tree.
func IsRepo(ctx context.Context, dir string) bool {
	out, err := Run(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}
