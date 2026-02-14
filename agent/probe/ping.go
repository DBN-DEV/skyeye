package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/time/rate"

	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/pkg/log"
)

const (
	// tickDivisor controls timer precision: tick = timeout / tickDivisor,
	// so the timing jitter is at most ~1/tickDivisor of the timeout.
	tickDivisor = 10

	// timeWheelSlots is the number of slots in the timer wheel.
	// Wheel capacity = tick * (slots - 2) = timeout * (slots - 2) / tickDivisor.
	// With 20 slots and divisor 10, capacity = 1.8 * timeout.
	timeWheelSlots = 20
)

var _ ContinuousTask = (*PingJob)(nil)

type PingJob struct {
	jobID uint64

	src  string
	dsts []netip.Addr

	timeout time.Duration
	limiter *rate.Limiter

	resultCh chan<- *pb.AgentMessage
	stopCh   chan struct{}

	timeWheel *timerWheel

	logger *zap.Logger
}

func (p *PingJob) Cancel() { close(p.stopCh) }

func NewContinuousPingTask(msg *pb.ContinuousPingJob, resultCh chan<- *pb.AgentMessage) (*PingJob, error) {
	dests := make([]netip.Addr, 0, len(msg.Destinations))
	for _, addr := range msg.GetDestinations() {
		ipAddr, ok := netip.AddrFromSlice(addr.Slice)
		if !ok {
			return nil, fmt.Errorf("ping: invalid destination address %s", addr.String())
		}
		dests = append(dests, ipAddr)
	}

	src, ok := netip.AddrFromSlice(msg.GetIp().Slice)
	if !ok {
		return nil, fmt.Errorf("ping: invalid source address %s", msg.GetIp().String())
	}
	interval := float64(msg.GetIntervalMs()) / 1000
	limit := float64(len(msg.GetDestinations())) / interval
	limiter := rate.NewLimiter(rate.Limit(limit), 1)

	timeout := time.Duration(msg.GetTimeoutMs()) * time.Millisecond
	tick := timeout / tickDivisor
	if tick == 0 {
		tick = time.Millisecond
	}

	return &PingJob{
		jobID:     msg.GetJobId(),
		src:       src.String(), // can not support port for now
		dsts:      dests,
		timeout:   timeout,
		limiter:   limiter,
		resultCh:  resultCh,
		stopCh:    make(chan struct{}),
		timeWheel: newTimerWheel(tick, timeWheelSlots),
		logger:    log.With(zap.Uint64("job_id", msg.GetJobId())),
	}, nil
}

func (p *PingJob) timeoutFn(dst netip.Addr) func() {
	return func() {
		p.resultCh <- &pb.AgentMessage{
			Payload: &pb.AgentMessage_ContinuousPingResult{ContinuousPingResult: &pb.ContinuousPingResult{
				JobId:       p.jobID,
				Destination: &pb.IP{Slice: dst.AsSlice()},
				Count:       1,
				Loss:        1,
				RttNano:     []int64{p.timeout.Nanoseconds()},
			}},
		}
	}
}

func (p *PingJob) Run() {
	for {
		select {
		case <-p.stopCh:
			return
		default:
			if err := p.run(); err != nil {
				p.logger.Error("ping job run failed", zap.Error(err))
			}
		}
	}
}

func (p *PingJob) run() error {
	conn, err := newPktConn()
	if err != nil {
		return fmt.Errorf("ping job: create packet conn: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Go(func() { p.loopRecv(ctx, conn.ipv4) })
	wg.Go(func() { p.loopRecv(ctx, conn.ipv6) })

	p.send(conn)

	go func() {
		select {
		case <-p.stopCh:
		case <-time.After(p.timeout):
		}

		cancel()

		wg.Wait()
		if err := conn.Close(); err != nil {
			p.logger.Error("ping job: close packet conn", zap.Error(err))
		}
	}()

	return nil
}

type pktConn struct {
	ipv4 net.PacketConn
	ipv6 net.PacketConn
}

func (p pktConn) WriteTo(b []byte, addr netip.Addr) (int, error) {
	if addr.Is4() {
		return p.ipv4.WriteTo(b, &net.UDPAddr{IP: addr.AsSlice()})
	}

	return p.ipv6.WriteTo(b, &net.UDPAddr{IP: addr.AsSlice()})
}

func (p pktConn) Close() error {
	var err error
	if e := p.ipv4.Close(); e != nil {
		err = errors.Join(err, e)
	}

	if e := p.ipv6.Close(); e != nil {
		err = errors.Join(err, e)
	}

	return err
}

func newPktConn() (pktConn, error) {
	ipv4Conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return pktConn{}, fmt.Errorf("probe: listen ipv4 icmp packet: %w", err)
	}

	ipv6Conn, err := icmp.ListenPacket("udp6", "::")
	if err != nil {
		return pktConn{}, fmt.Errorf("probe: listen ipv6 icmp packet: %w", err)
	}

	return pktConn{ipv4: ipv4Conn, ipv6: ipv6Conn}, nil
}

func (p *PingJob) send(conn pktConn) {
	for _, dst := range p.dsts {
		if err := p.limiter.Wait(context.Background()); err != nil {
			p.logger.Warn("ping job stopped", zap.Error(err))
			return
		}

		timeoutFn := p.timeoutFn(dst)
		id, err := p.timeWheel.Add(p.timeout, timeoutFn)
		if err != nil {
			p.logger.Error("failed to add timeout timer", zap.Error(err))
			continue
		}

		pl := &payload{Time: time.Now(), ID: id}
		plBytes, err := pl.marshal()
		if err != nil {
			p.logger.Error("failed to marshal payload", zap.Error(err))
			_ = p.timeWheel.Cancel(id)
			continue
		}
		pkt := newPkt(dst, plBytes)
		b, err := pkt.Marshal(nil)
		if err != nil {
			p.logger.Error("failed to marshal icmp packet", zap.Error(err))
			_ = p.timeWheel.Cancel(id)
			continue
		}

		_, err = conn.WriteTo(b, dst)
		if err != nil {
			p.logger.Error("failed to send icmp packet", zap.Error(err))
			_ = p.timeWheel.Cancel(id)
			continue
		}
	}
}

func parsePkt(src netip.Addr, data []byte) ([]byte, error) {
	proto := ipv4.ICMPTypeEcho.Protocol()
	if src.Is6() {
		proto = ipv6.ICMPTypeEchoRequest.Protocol()
	}

	msg, err := icmp.ParseMessage(proto, data)
	if err != nil {
		return nil, fmt.Errorf("probe: parse icmp message: %w", err)
	}

	if msg.Type != ipv4.ICMPTypeEchoReply && msg.Type != ipv6.ICMPTypeEchoReply {
		return nil, fmt.Errorf("probe: invalid icmp message type: %v", msg.Type)
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		return nil, fmt.Errorf("probe: invalid icmp echo body, got %T", msg.Body)
	}

	return echo.Data, nil
}

func (p *PingJob) recv(conn net.PacketConn) error {
	buf := make([]byte, 1500)
	if err := conn.SetReadDeadline(time.Now().Add(p.timeout)); err != nil {
		return fmt.Errorf("probe: set read deadline: %w", err)
	}

	n, addr, err := conn.ReadFrom(buf)
	if err != nil {
		return fmt.Errorf("probe: read from conn: %w", err)
	}

	ipAddr, ok := netip.AddrFromSlice(addr.(*net.UDPAddr).IP)
	if !ok {
		return fmt.Errorf("probe: invalid source address %s", addr.String())
	}

	data, err := parsePkt(ipAddr, buf[:n])
	if err != nil {
		return fmt.Errorf("probe: parse packet: %w", err)
	}

	pl, err := unmarshalPayload(data)
	if err != nil {
		return fmt.Errorf("probe: unmarshal payload: %w", err)
	}

	if err := p.timeWheel.Cancel(pl.ID); err != nil {
		return fmt.Errorf("probe: cancel timeout timer: %w", err)
	}

	rtt := time.Since(pl.Time)
	if rtt > p.timeout {
		return fmt.Errorf("probe: %s rtt %s exceeds timeout %s", ipAddr.String(), rtt, p.timeout)
	}

	p.resultCh <- &pb.AgentMessage{
		Payload: &pb.AgentMessage_ContinuousPingResult{ContinuousPingResult: &pb.ContinuousPingResult{
			JobId:       p.jobID,
			Destination: &pb.IP{Slice: ipAddr.AsSlice()},
			Count:       1,
			Loss:        0,
			RttNano:     []int64{rtt.Nanoseconds()},
		}},
	}

	return nil
}

func (p *PingJob) loopRecv(ctx context.Context, conn net.PacketConn) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := p.recv(conn); err != nil {
				p.logger.Warn("recv ping reply failed", zap.Error(err))
			}
		}
	}
}

const _payloadLen = _idLen + 8 // 8 bytes for time

type payload struct {
	Time time.Time
	ID   []byte
}

func (p *payload) marshal() ([]byte, error) {
	if len(p.ID) != _idLen {
		return nil, fmt.Errorf("probe: invalid id length: %d", len(p.ID))
	}

	data := make([]byte, 0, _payloadLen)
	data = append(binary.LittleEndian.AppendUint64(data, uint64(p.Time.UnixNano())), p.ID...)
	return data, nil
}

func unmarshalPayload(data []byte) (*payload, error) {
	if len(data) != _payloadLen {
		return nil, fmt.Errorf("probe: invalid payload length: %d", len(data))
	}

	nano := int64(binary.LittleEndian.Uint64(data[:8]))

	t := time.Unix(0, nano)
	id := data[8 : 8+_idLen]

	return &payload{Time: t, ID: id}, nil
}

func newPkt(dst netip.Addr, data []byte) *icmp.Message {
	echo := &icmp.Echo{Seq: rand.Int(), Data: data}

	typ := icmp.Type(ipv4.ICMPTypeEcho)
	if dst.Is6() {
		typ = ipv6.ICMPTypeEchoRequest
	}

	return &icmp.Message{Type: typ, Body: echo}
}
