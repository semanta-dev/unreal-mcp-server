package uexec

import (
	"context"
	"errors"
)

// RetryPolicy decides whether a command may be transparently re-sent after the
// connection is lost AFTER it was written (plan §2.8 connection-loss table, case 3).
// A command whose write failed never reached the editor and is always re-sendable.
type RetryPolicy int

const (
	// RetryNone (the default) never re-sends a command that may have executed: a
	// read-side loss after a successful write returns ErrOutcomeUnknown.
	RetryNone RetryPolicy = iota
	// RetryIdempotent re-sends once after a read-side loss: for read-only or
	// idempotent commands, running twice is harmless.
	RetryIdempotent
)

type retryCtxKey struct{}

// WithRetryPolicy returns ctx carrying the retry policy for the commands run under it.
func WithRetryPolicy(ctx context.Context, p RetryPolicy) context.Context {
	return context.WithValue(ctx, retryCtxKey{}, p)
}

func retryPolicy(ctx context.Context) RetryPolicy {
	p, _ := ctx.Value(retryCtxKey{}).(RetryPolicy)
	return p
}

var (
	// ErrOutcomeUnknown: the connection was lost after the command was written, so
	// the editor may or may not have executed it. Never retried for RetryNone
	// commands. Wraps ErrConnectionLost.
	ErrOutcomeUnknown = errors.New("connection lost after the command was sent; outcome unknown")

	// ErrChannelStolen: another client holds this editor's single command slot (a
	// peer close repeated within the theft window while the same editor still
	// answers discovery). The session stops reconnecting until Reclaim.
	ErrChannelStolen = errors.New("another MCP server is connected to this editor (command channel stolen)")
)
