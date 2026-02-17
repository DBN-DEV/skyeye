package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

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

	recvReadTimeout = 200 * time.Millisecond
)

var _ ContinuousTask = (*PingJob)(nil)

type PingJob struct {
	jobID uint64

	src4 string
	src6 string

	dsts4 []netip.Addr
	dsts6 []netip.Addr

	interval time.Duration
	timeout  time.Duration
	count    uint32

	resultCh chan<- *pb.AgentMessage
	stopCh   chan struct{}
	stopOnce sync.Once

	timeWheel *timerWheel
	nextRound atomic.Uint64

	mu       sync.Mutex
	rounds   map[uint64]map[string]*ipRoundState
	inFlight map[uint64]*inFlightPacket

	logger *zap.Logger
}

type ipRoundState struct {
	dst netip.Addr

	sent      uint32
	recv      uint32
	timeout   uint32
	sendError uint32

	rttSum int64
	rttMin int64
	rttMax int64

	closed bool
}

type inFlightPacket struct {
	roundID  uint64
	dst      netip.Addr
	timerID  timeWheelID
	hasTimer bool
}

func (p *PingJob) Cancel() {
	p.stopOnce.Do(func() { close(p.stopCh) })
}

func NewPingTask(msg *pb.PingJob, resultCh chan<- *pb.AgentMessage) (*PingJob, error) {
	dsts4 := make([]netip.Addr, 0, len(msg.Destinations))
	dsts6 := make([]netip.Addr, 0, len(msg.Destinations))
	for _, addr := range msg.GetDestinations() {
		ipAddr, ok := netip.AddrFromSlice(addr.Slice)
		if !ok {
			return nil, fmt.Errorf("ping: invalid destination address %s", addr.String())
		}
		if ipAddr.Is4() {
			dsts4 = append(dsts4, ipAddr)
		} else {
			dsts6 = append(dsts6, ipAddr)
		}
	}
	if len(dsts4)+len(dsts6) == 0 {
		return nil, errors.New("ping: no valid destination addresses")
	}

	src4 := "0.0.0.0"
	src6 := "::"
	if src := msg.GetIp(); src != nil {
		srcAddr, ok := netip.AddrFromSlice(src.Slice)
		if !ok {
			return nil, fmt.Errorf("ping: invalid source address %s", src.String())
		}
		if srcAddr.Is4() {
			src4 = srcAddr.String()
		} else {
			src6 = srcAddr.String()
		}
	}

	interval := time.Duration(msg.GetIntervalMs()) * time.Millisecond
	if interval <= 0 {
		return nil, errors.New("ping: interval_ms must be greater than zero")
	}
	timeout := time.Duration(msg.GetTimeoutMs()) * time.Millisecond
	if timeout <= 0 {
		return nil, errors.New("ping: timeout_ms must be greater than zero")
	}
	count := msg.GetCount()
	if count == 0 {
		count = 1
	}

	tick := timeout / tickDivisor
	if tick == 0 {
		tick = time.Millisecond
	}

	return &PingJob{
		jobID:     msg.GetJobId(),
		src4:      src4,
		src6:      src6,
		dsts4:     dsts4,
		dsts6:     dsts6,
		interval:  interval,
		timeout:   timeout,
		count:     count,
		resultCh:  resultCh,
		stopCh:    make(chan struct{}),
		timeWheel: newTimerWheel(tick, timeWheelSlots),
		rounds:    make(map[uint64]map[string]*ipRoundState),
		inFlight:  make(map[uint64]*inFlightPacket),
		logger:    log.With(zap.Uint64("job_id", msg.GetJobId())),
	}, nil
}

func (p *PingJob) timeoutFn(token uint64) func() {
	return func() {
		msg := p.handleTimeout(token)
		if msg != nil {
			p.emitResult(msg)
		}
	}
}

func (p *PingJob) Run() {
	conn, err := newPktConn(len(p.dsts4) > 0, p.src4, len(p.dsts6) > 0, p.src6)
	if err != nil {
		p.logger.Error("ping job: create raw packet conn failed", zap.Error(err))
		return
	}
	defer p.timeWheel.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	if conn.ipv4 != nil {
		wg.Go(func() { p.sendLoop(ctx, conn.ipv4, p.dsts4) })
		wg.Go(func() { p.recvLoop(ctx, conn.ipv4) })
	}
	if conn.ipv6 != nil {
		wg.Go(func() { p.sendLoop(ctx, conn.ipv6, p.dsts6) })
		wg.Go(func() { p.recvLoop(ctx, conn.ipv6) })
	}

	<-p.stopCh
	cancel()
	if err := conn.Close(); err != nil {
		p.logger.Warn("ping job: close raw packet conn", zap.Error(err))
	}
	wg.Wait()
	p.flushPendingOnStop()
}

type pktConn struct {
	ipv4 net.PacketConn
	ipv6 net.PacketConn
}

func (p pktConn) Close() error {
	var err error
	if p.ipv4 != nil {
		if e := p.ipv4.Close(); e != nil {
			err = errors.Join(err, e)
		}
	}
	if p.ipv6 != nil {
		if e := p.ipv6.Close(); e != nil {
			err = errors.Join(err, e)
		}
	}
	return err
}

func newPktConn(enable4 bool, src4 string, enable6 bool, src6 string) (pktConn, error) {
	if !enable4 && !enable6 {
		return pktConn{}, errors.New("probe: no destination family enabled")
	}

	var conn pktConn
	if enable4 {
		ipv4Conn, err := icmp.ListenPacket("ip4:icmp", src4)
		if err != nil {
			return pktConn{}, fmt.Errorf("probe: listen raw ipv4 icmp packet: %w", err)
		}
		conn.ipv4 = ipv4Conn
	}

	if enable6 {
		ipv6Conn, err := icmp.ListenPacket("ip6:ipv6-icmp", src6)
		if err != nil {
			_ = conn.Close()
			return pktConn{}, fmt.Errorf("probe: listen raw ipv6 icmp packet: %w", err)
		}
		conn.ipv6 = ipv6Conn
	}

	return conn, nil
}

func (p *PingJob) sendLoop(ctx context.Context, conn net.PacketConn, dsts []netip.Addr) {
	if len(dsts) == 0 {
		return
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		roundID := p.nextRound.Add(1)
		p.sendRound(ctx, conn, dsts, roundID)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *PingJob) sendRound(ctx context.Context, conn net.PacketConn, dsts []netip.Addr, roundID uint64) {
	for _, dst := range dsts {
		p.initRoundState(roundID, dst)
	}

	pace := p.interval / time.Duration(p.count)

	for i := uint32(0); i < p.count; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pace):
			}
		}

		for _, dst := range dsts {
			if err := p.sendProbe(conn, dst, roundID); err != nil {
				p.logger.Warn(
					"send ping packet failed",
					zap.Uint64("round_id", roundID),
					zap.String("destination", dst.String()),
					zap.Error(err),
				)
				p.recordSendError(roundID, dst)
			}
		}
	}

	for _, dst := range dsts {
		if msg := p.closeRoundAndBuildResult(roundID, dst); msg != nil {
			p.emitResult(msg)
		}
	}
}

func (p *PingJob) sendProbe(conn net.PacketConn, dst netip.Addr, roundID uint64) error {
	token := rand.Uint64()
	pl := &payload{Time: time.Now(), Token: token}
	pkt := newPkt(dst, pl.marshal())
	b, err := pkt.Marshal(nil)
	if err != nil {
		return fmt.Errorf("probe: marshal icmp packet: %w", err)
	}

	if _, err := conn.WriteTo(b, &net.IPAddr{IP: dst.AsSlice()}); err != nil {
		return fmt.Errorf("probe: send icmp packet: %w", err)
	}

	p.recordSent(roundID, dst)
	p.putInFlight(token, &inFlightPacket{roundID: roundID, dst: dst})

	id, err := p.timeWheel.Add(p.timeout, p.timeoutFn(token))
	if err != nil {
		p.logger.Error("failed to add timeout timer", zap.Uint64("token", token), zap.Error(err))
		_, _ = p.takeInFlight(token)
		if msg := p.recordTimeoutAndBuildResult(roundID, dst); msg != nil {
			p.emitResult(msg)
		}
		return nil
	}
	if !p.bindInFlightTimer(token, id) {
		_ = p.timeWheel.Cancel(id)
	}

	return nil
}

func (p *PingJob) recvLoop(ctx context.Context, conn net.PacketConn) {
	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(recvReadTimeout)); err != nil {
			p.logger.Warn("set read deadline failed", zap.Error(err))
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
			p.logger.Warn("recv ping reply failed", zap.Error(err))
			continue
		}

		msg := p.handleRecv(addr, buf[:n])
		if msg != nil {
			p.emitResult(msg)
		}
	}
}

func (p *PingJob) handleRecv(addr net.Addr, raw []byte) *pb.AgentMessage {
	ipAddr, err := addrToIP(addr)
	if err != nil {
		p.logger.Warn("invalid source address", zap.Error(err))
		return nil
	}

	data, err := parsePkt(ipAddr, raw)
	if err != nil {
		p.logger.Debug("parse packet failed", zap.Error(err))
		return nil
	}

	pl, err := unmarshalPayload(data)
	if err != nil {
		p.logger.Debug("unmarshal payload failed", zap.Error(err))
		return nil
	}

	meta, ok := p.takeInFlight(pl.Token)
	if !ok {
		return nil
	}
	if meta.hasTimer {
		_ = p.timeWheel.Cancel(meta.timerID)
	}

	rtt := time.Since(pl.Time)
	if rtt < 0 {
		rtt = 0
	}
	if rtt > p.timeout {
		return p.recordTimeoutAndBuildResult(meta.roundID, meta.dst)
	}

	return p.recordRecvAndBuildResult(meta.roundID, meta.dst, rtt)
}

func (p *PingJob) handleTimeout(token uint64) *pb.AgentMessage {
	meta, ok := p.takeInFlight(token)
	if !ok {
		return nil
	}
	return p.recordTimeoutAndBuildResult(meta.roundID, meta.dst)
}

func addrToIP(addr net.Addr) (netip.Addr, error) {
	switch a := addr.(type) {
	case *net.IPAddr:
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			return netip.Addr{}, fmt.Errorf("invalid ip addr: %s", a.String())
		}
		return ip, nil
	case *net.UDPAddr:
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			return netip.Addr{}, fmt.Errorf("invalid udp addr: %s", a.String())
		}
		return ip, nil
	default:
		return netip.Addr{}, fmt.Errorf("unsupported addr type: %T", addr)
	}
}

func (p *PingJob) initRoundState(roundID uint64, dst netip.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureRoundStateLocked(roundID, dst)
}

func (p *PingJob) recordSent(roundID uint64, dst netip.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.ensureRoundStateLocked(roundID, dst)
	state.sent++
}

func (p *PingJob) recordSendError(roundID uint64, dst netip.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.ensureRoundStateLocked(roundID, dst)
	state.sendError++
}

func (p *PingJob) closeRoundAndBuildResult(roundID uint64, dst netip.Addr) *pb.AgentMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := dst.String()
	state := p.ensureRoundStateLocked(roundID, dst)
	state.closed = true
	return p.buildRoundResultIfReadyLocked(roundID, key, state)
}

func (p *PingJob) recordRecvAndBuildResult(roundID uint64, dst netip.Addr, rtt time.Duration) *pb.AgentMessage {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := dst.String()
	state := p.ensureRoundStateLocked(roundID, dst)
	state.recv++

	nano := rtt.Nanoseconds()
	state.rttSum += nano
	if state.recv == 1 {
		state.rttMin = nano
		state.rttMax = nano
	} else {
		if nano < state.rttMin {
			state.rttMin = nano
		}
		if nano > state.rttMax {
			state.rttMax = nano
		}
	}

	return p.buildRoundResultIfReadyLocked(roundID, key, state)
}

func (p *PingJob) recordTimeoutAndBuildResult(roundID uint64, dst netip.Addr) *pb.AgentMessage {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := dst.String()
	state := p.ensureRoundStateLocked(roundID, dst)
	state.timeout++
	return p.buildRoundResultIfReadyLocked(roundID, key, state)
}

func (p *PingJob) ensureRoundStateLocked(roundID uint64, dst netip.Addr) *ipRoundState {
	round, ok := p.rounds[roundID]
	if !ok {
		round = make(map[string]*ipRoundState)
		p.rounds[roundID] = round
	}
	key := dst.String()
	state, ok := round[key]
	if !ok {
		state = &ipRoundState{dst: dst}
		round[key] = state
	}
	return state
}

func (p *PingJob) buildRoundResultIfReadyLocked(roundID uint64, key string, state *ipRoundState) *pb.AgentMessage {
	if !state.closed {
		return nil
	}
	if state.recv+state.timeout != state.sent {
		return nil
	}

	lossRate := 0.0
	if state.sent > 0 {
		lossRate = float64(state.timeout) / float64(state.sent)
	}

	minRTT := int64(0)
	avgRTT := int64(0)
	maxRTT := int64(0)
	if state.recv > 0 {
		minRTT = state.rttMin
		maxRTT = state.rttMax
		avgRTT = state.rttSum / int64(state.recv)
	}

	result := &pb.PingRoundResult{
		JobId: p.jobID,
		Destination: &pb.IP{
			Slice: state.dst.AsSlice(),
		},
		Sent:       state.sent,
		Recv:       state.recv,
		Timeout:    state.timeout,
		SendError:  state.sendError,
		LossRate:   lossRate,
		MinRttNano: minRTT,
		AvgRttNano: avgRTT,
		MaxRttNano: maxRTT,
	}

	if round := p.rounds[roundID]; round != nil {
		delete(round, key)
		if len(round) == 0 {
			delete(p.rounds, roundID)
		}
	}

	return &pb.AgentMessage{
		Payload: &pb.AgentMessage_PingRoundResult{
			PingRoundResult: result,
		},
	}
}

func (p *PingJob) putInFlight(token uint64, meta *inFlightPacket) {
	p.mu.Lock()
	p.inFlight[token] = meta
	p.mu.Unlock()
}

func (p *PingJob) bindInFlightTimer(token uint64, timerID timeWheelID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	meta, ok := p.inFlight[token]
	if !ok {
		return false
	}
	meta.hasTimer = true
	meta.timerID = timerID
	return true
}

func (p *PingJob) takeInFlight(token uint64) (*inFlightPacket, bool) {
	p.mu.Lock()
	meta, ok := p.inFlight[token]
	if ok {
		delete(p.inFlight, token)
	}
	p.mu.Unlock()
	return meta, ok
}

func (p *PingJob) flushPendingOnStop() {
	metas := p.drainInFlight()
	for _, meta := range metas {
		if meta.hasTimer {
			_ = p.timeWheel.Cancel(meta.timerID)
		}
		if msg := p.recordTimeoutAndBuildResult(meta.roundID, meta.dst); msg != nil {
			p.emitResult(msg)
		}
	}

	for _, msg := range p.closeAllRoundsAndCollectResults() {
		p.emitResult(msg)
	}
}

func (p *PingJob) drainInFlight() []*inFlightPacket {
	p.mu.Lock()
	defer p.mu.Unlock()

	metas := make([]*inFlightPacket, 0, len(p.inFlight))
	for token, meta := range p.inFlight {
		metas = append(metas, meta)
		delete(p.inFlight, token)
	}
	return metas
}

func (p *PingJob) closeAllRoundsAndCollectResults() []*pb.AgentMessage {
	p.mu.Lock()
	defer p.mu.Unlock()

	results := make([]*pb.AgentMessage, 0)
	for roundID, round := range p.rounds {
		for key, state := range round {
			state.closed = true
			if msg := p.buildRoundResultIfReadyLocked(roundID, key, state); msg != nil {
				results = append(results, msg)
			}
		}
	}
	return results
}

func (p *PingJob) emitResult(msg *pb.AgentMessage) {
	select {
	case p.resultCh <- msg:
		return
	default:
	}
	select {
	case p.resultCh <- msg:
	case <-p.stopCh:
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

const _payloadLen = 16 // 8 bytes for time + 8 bytes token

type payload struct {
	Time  time.Time
	Token uint64
}

func (p *payload) marshal() []byte {
	data := make([]byte, 0, _payloadLen)
	data = binary.LittleEndian.AppendUint64(data, uint64(p.Time.UnixNano()))
	data = binary.LittleEndian.AppendUint64(data, p.Token)
	return data
}

func unmarshalPayload(data []byte) (*payload, error) {
	if len(data) != _payloadLen {
		return nil, fmt.Errorf("probe: invalid payload length: %d", len(data))
	}

	nano := int64(binary.LittleEndian.Uint64(data[:8]))
	token := binary.LittleEndian.Uint64(data[8:16])
	return &payload{Time: time.Unix(0, nano), Token: token}, nil
}

func newPkt(dst netip.Addr, data []byte) *icmp.Message {
	echo := &icmp.Echo{ID: rand.IntN(1 << 16), Seq: rand.IntN(1 << 16), Data: data}

	typ := icmp.Type(ipv4.ICMPTypeEcho)
	if dst.Is6() {
		typ = ipv6.ICMPTypeEchoRequest
	}

	return &icmp.Message{Type: typ, Body: echo}
}
