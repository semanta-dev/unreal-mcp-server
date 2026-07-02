package uexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"
)

// commandConn is a single TCP command channel to one editor node. It is NOT
// internally synchronized; the Session provides single-flight serialization
// (the reference client's global _LOCK).
type commandConn struct {
	cfg    Config
	self   string
	remote string
	bc     *broadcastConn
	logger *slog.Logger

	conn    net.Conn
	dec     *json.Decoder
	tainted bool
}

// open performs the reverse-connect: listen on the command port, broadcast
// open_connection advertising that port, and accept the editor's connect-back.
// Retries AcceptAttempts×AcceptTimeout, re-broadcasting each attempt.
func (c *commandConn) open(ctx context.Context) error {
	ln, err := listenTCP(ctx, c.cfg.CommandAddr)
	if err != nil {
		return fmt.Errorf("%w: listen %s: %v", ErrConnectionLost, c.cfg.CommandAddr, err)
	}
	ip, port := advertisedEndpoint(c.cfg, ln)

	var lastErr error
	for i := 0; i < c.cfg.AcceptAttempts; i++ {
		if ctx.Err() != nil {
			ln.Close()
			return ctx.Err()
		}
		if err := c.bc.broadcastOpenConnection(c.remote, ip, port); err != nil {
			lastErr = err
		}
		if d, ok := ln.(interface{ SetDeadline(time.Time) error }); ok {
			_ = d.SetDeadline(time.Now().Add(c.cfg.AcceptTimeout))
		}
		conn, aerr := ln.Accept()
		if aerr != nil {
			lastErr = aerr
			continue // timeout or transient; re-broadcast and retry
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
		c.conn = conn
		// One long-lived Decoder for the connection's life. JSON values are
		// self-delimiting, so this is immune to TCP segmentation and to the
		// exact-multiple-of-8192 hang in the reference recv-until-short-read loop.
		c.dec = json.NewDecoder(bufio.NewReaderSize(conn, 65536))
		ln.Close() // channel is 1:1; stop holding the port once connected
		c.logger.Info("command channel established", "node_id", c.remote, "local", conn.LocalAddr().String(), "remote", conn.RemoteAddr().String())
		return nil
	}
	ln.Close()
	return fmt.Errorf("%w: editor did not connect back after %d attempts (editor busy or unreachable; a previous command may still be running on the game thread): %v",
		ErrConnectionLost, c.cfg.AcceptAttempts, lastErr)
}

// advertisedEndpoint is the ip/port we tell the editor to connect back to: the
// actually-bound port, and a loopback host (never 0.0.0.0).
func advertisedEndpoint(cfg Config, ln net.Listener) (string, int) {
	port := ln.Addr().(*net.TCPAddr).Port
	host, _, err := net.SplitHostPort(cfg.CommandAddr)
	if err != nil || host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return host, port
}

// runCommand sends one command and reads its result. On timeout/cancel or any
// transport error the connection is tainted (closed, never reused) so an
// abandoned command's late reply cannot desync the next connection.
func (c *commandConn) runCommand(ctx context.Context, code string, mode ExecMode, timeout time.Duration) (CommandResult, error) {
	if c.tainted || c.conn == nil {
		return CommandResult{}, ErrConnectionLost
	}
	// ExecuteFile mis-parses code starting with a quoted string as a file path;
	// a leading comment avoids it (reference client parity). Applied once, here.
	if mode == ModeExecFile && !strings.HasPrefix(code, "# mcp\n") {
		code = "# mcp\n" + code
	}
	data, err := marshalNoEscape(commandData{Command: code, Unattended: true, ExecMode: string(mode)})
	if err != nil {
		return CommandResult{}, fmt.Errorf("%w: marshal command: %v", ErrProtocol, err)
	}
	raw, err := newMessage(TypeCommand, c.self, c.remote, data).encode()
	if err != nil {
		return CommandResult{}, fmt.Errorf("%w: encode command: %v", ErrProtocol, err)
	}

	if err := c.write(ctx, raw, timeout); err != nil {
		c.taint()
		return CommandResult{}, err
	}
	resp, err := c.readMessage(ctx, timeout)
	if err != nil {
		c.taint()
		return CommandResult{}, err
	}
	// Validate the reply: addressed to us, correct type, AND from the node we
	// targeted. The source check is the result-spoofing defense (§12.4): a rogue
	// same-user local process that connected to our listener would have to also
	// forge the discovered editor's node id, which requires already reading our
	// loopback traffic. A mismatch degrades to a tainted command, never a wrong result.
	if !resp.passesFilter(c.self) || resp.Type != TypeCommandResult || resp.Source != c.remote {
		c.taint()
		return CommandResult{}, fmt.Errorf("%w: expected command_result from %s, got type=%q source=%q dest=%q",
			ErrProtocol, c.remote, resp.Type, resp.Source, resp.Dest)
	}
	var cr CommandResult
	if err := json.Unmarshal(resp.Data, &cr); err != nil {
		c.taint()
		return CommandResult{}, fmt.Errorf("%w: decode command_result: %v", ErrProtocol, err)
	}
	return cr, nil
}

func (c *commandConn) write(ctx context.Context, b []byte, timeout time.Duration) error {
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.conn.SetWriteDeadline(time.Now())
		case <-stop:
		}
	}()
	defer close(stop)

	if timeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(timeout))
	} else {
		_ = c.conn.SetWriteDeadline(time.Time{})
	}
	if _, err := c.conn.Write(b); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return fmt.Errorf("%w: write", ErrTimeout)
		}
		return fmt.Errorf("%w: write: %v", ErrConnectionLost, err)
	}
	return nil
}

func (c *commandConn) readMessage(ctx context.Context, timeout time.Duration) (Message, error) {
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.conn.SetReadDeadline(time.Now())
		case <-stop:
		}
	}()
	defer close(stop)

	if timeout > 0 {
		_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	} else {
		_ = c.conn.SetReadDeadline(time.Time{})
	}
	var m Message
	err := c.dec.Decode(&m)
	if err != nil {
		if ctx.Err() != nil {
			return m, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return m, fmt.Errorf("%w: read", ErrTimeout)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return m, fmt.Errorf("%w: read: %v", ErrConnectionLost, err)
		}
		return m, fmt.Errorf("%w: decode: %v", ErrProtocol, err)
	}
	if err := m.validate(); err != nil {
		return m, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return m, nil
}

func (c *commandConn) taint() {
	c.tainted = true
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *commandConn) close() {
	c.tainted = true
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}
