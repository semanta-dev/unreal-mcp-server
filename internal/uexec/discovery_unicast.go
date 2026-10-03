package uexec

import (
	"context"
	"log/slog"
	"net"
)

// OpenUnicastDiscovery starts a Discovery that pings a single editor endpoint over
// unicast loopback UDP instead of joining the multicast group. It exists for
// in-process end-to-end tests that drive a fake editor (no multicast, parallel-safe);
// production uses OpenDiscovery. Sessions attach with NewOnDiscovery as usual.
func OpenUnicastDiscovery(ctx context.Context, cfg Config, target net.Addr, logger *slog.Logger) (*Discovery, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	cfg = cfg.withDefaults()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	self := newUUID()
	bc := newBroadcastConn(self, pc, target, cfg.PingInterval, cfg.NodeTimeout, logger)
	bc.start(ctx)
	return &Discovery{bc: bc, self: self}, nil
}
