package uexec

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"syscall"

	"golang.org/x/net/ipv4"
)

// controlReuseAddr is a net.ListenConfig.Control hook that sets SO_REUSEADDR
// BEFORE bind. Both the UDP discovery port (6766) and the TCP command port
// (6776) are also held by the editor/other clients, so REUSEADDR is required to
// bind. Windows has no SO_REUSEPORT — REUSEADDR is the correct branch there
// (mirrors the reference client). setReuseAddr is platform-specific (fd cast).
func controlReuseAddr(network, address string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) { serr = setReuseAddr(fd) }); err != nil {
		return err
	}
	return serr
}

// loopbackInterface returns the up loopback interface, or nil if none is found
// (JoinGroup then falls back to the system default). TTL 0 requires the loopback
// interface for pings to reach a same-host editor, so this is load-bearing.
func loopbackInterface() *net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for i := range ifaces {
		f := ifaces[i].Flags
		if f&net.FlagLoopback != 0 && f&net.FlagUp != 0 {
			return &ifaces[i]
		}
	}
	return nil
}

// newMulticastConn opens the UDP discovery socket exactly like the reference
// client: SO_REUSEADDR before bind, join the multicast group on the loopback
// interface, IP_MULTICAST_IF=loopback, LOOP=1, TTL as configured (0=localhost).
// Returns the packet conn (for send/recv) and the group send address.
func newMulticastConn(ctx context.Context, cfg Config) (net.PacketConn, *net.UDPAddr, *net.Interface, error) {
	group, err := net.ResolveUDPAddr("udp4", cfg.MulticastGroup)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve multicast group %q: %w", cfg.MulticastGroup, err)
	}
	if !group.IP.IsMulticast() {
		// Unicast discovery: ping one editor endpoint directly (a fake editor in the
		// binary smoke tests, or an editor reached by address rather than multicast).
		pc, err := net.ListenPacket("udp4", net.JoinHostPort(cfg.BindAddress, "0"))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("bind udp for unicast discovery: %w", err)
		}
		return pc, group, nil, nil
	}
	bindAddr := net.JoinHostPort(cfg.BindAddress, strconv.Itoa(group.Port))

	lc := net.ListenConfig{Control: controlReuseAddr}
	pc, err := lc.ListenPacket(ctx, "udp4", bindAddr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bind udp %s: %w", bindAddr, err)
	}

	lo := loopbackInterface()
	p := ipv4.NewPacketConn(pc)
	// Best-effort options: some are not fatal if unsupported, but a failed join is.
	_ = p.SetMulticastInterface(lo)
	_ = p.SetMulticastLoopback(true)
	_ = p.SetMulticastTTL(cfg.MulticastTTL)
	if err := p.JoinGroup(lo, &net.UDPAddr{IP: group.IP}); err != nil {
		pc.Close()
		return nil, nil, nil, fmt.Errorf("join multicast group %s on loopback: %w", group.IP, err)
	}
	return pc, group, lo, nil
}

// listenTCP opens the reverse-connect command listener with SO_REUSEADDR.
func listenTCP(ctx context.Context, addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: controlReuseAddr}
	return lc.Listen(ctx, "tcp4", addr)
}
