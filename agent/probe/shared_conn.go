package probe

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/icmp"

	"github.com/DBN-DEV/skyeye/pkg/log"
)

const sharedConnReadTimeout = 200 * time.Millisecond

type SharedConn struct {
	ipv4 net.PacketConn
	ipv6 net.PacketConn

	mu        sync.RWMutex
	callbacks map[uint64]func(sendTime time.Time)

	logger *zap.Logger
}

func NewSharedConn() (*SharedConn, error) {
	logger := log.With(zap.String("component", "shared-conn"))

	ipv4Conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		logger.Warn("ipv4 icmp unavailable, ipv4 probes disabled", zap.Error(err))
	}

	ipv6Conn, err := icmp.ListenPacket("ip6:ipv6-icmp", "::")
	if err != nil {
		logger.Warn("ipv6 icmp unavailable, ipv6 probes disabled", zap.Error(err))
	}

	if ipv4Conn == nil && ipv6Conn == nil {
		return nil, errors.New("probe: failed to open any ICMP socket")
	}

	return &SharedConn{
		ipv4:      ipv4Conn,
		ipv6:      ipv6Conn,
		callbacks: make(map[uint64]func(sendTime time.Time)),
		logger:    logger,
	}, nil
}

func (sc *SharedConn) IPv4() net.PacketConn { return sc.ipv4 }
func (sc *SharedConn) IPv6() net.PacketConn { return sc.ipv6 }

func (sc *SharedConn) Register(token uint64, cb func(sendTime time.Time)) {
	sc.mu.Lock()
	sc.callbacks[token] = cb
	sc.mu.Unlock()
}

func (sc *SharedConn) Unregister(token uint64) {
	sc.mu.Lock()
	delete(sc.callbacks, token)
	sc.mu.Unlock()
}

func (sc *SharedConn) Run(ctx context.Context) {
	var wg sync.WaitGroup
	if sc.ipv4 != nil {
		wg.Go(func() { sc.recvLoop(ctx, sc.ipv4, false) })
	}
	if sc.ipv6 != nil {
		wg.Go(func() { sc.recvLoop(ctx, sc.ipv6, true) })
	}

	<-ctx.Done()

	if sc.ipv4 != nil {
		if err := sc.ipv4.Close(); err != nil {
			sc.logger.Warn("close ipv4 conn", zap.Error(err))
		}
	}
	if sc.ipv6 != nil {
		if err := sc.ipv6.Close(); err != nil {
			sc.logger.Warn("close ipv6 conn", zap.Error(err))
		}
	}

	wg.Wait()
}

func (sc *SharedConn) recvLoop(ctx context.Context, conn net.PacketConn, isV6 bool) {
	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(sharedConnReadTimeout)); err != nil {
			sc.logger.Warn("set read deadline failed", zap.Error(err))
			return
		}

		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			sc.logger.Warn("recv icmp reply failed", zap.Error(err))
			continue
		}

		ipAddr, err := addrToIP(addr)
		if err != nil {
			sc.logger.Warn("invalid source address", zap.Error(err))
			continue
		}

		if isV6 {
			ipAddr = netip.AddrFrom16(ipAddr.As16())
		}

		data, err := parsePkt(ipAddr, buf[:n])
		if err != nil {
			sc.logger.Debug("parse packet failed", zap.Error(err))
			continue
		}

		pl, err := unmarshalPayload(data)
		if err != nil {
			sc.logger.Debug("unmarshal payload failed", zap.Error(err))
			continue
		}

		sc.mu.RLock()
		cb, ok := sc.callbacks[pl.Token]
		sc.mu.RUnlock()

		if ok {
			go cb(pl.Time)
		}
	}
}
