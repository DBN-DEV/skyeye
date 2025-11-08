package probe

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/time/rate"

	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/pkg/log"
)

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

	return &PingJob{
		src:      src.String(), // can not support port for now
		dsts:     dests,
		timeout:  time.Duration(msg.GetTimeoutMs()) * time.Millisecond,
		limiter:  limiter,
		resultCh: resultCh,
		stopCh:   make(chan struct{}),
		logger:   log.With(zap.Uint64("job_id", msg.GetJobId())),
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

func (p *PingJob) send(conn net.PacketConn) {
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
		pkt := p.newPkt(dst, plBytes)
		b, err := pkt.Marshal(nil)
		if err != nil {
			p.logger.Error("failed to marshal icmp packet", zap.Error(err))
			_ = p.timeWheel.Cancel(id)
			continue
		}

		_, err = conn.WriteTo(b, &net.UDPAddr{IP: dst.AsSlice()})
		if err != nil {
			p.logger.Error("failed to send icmp packet", zap.Error(err))
			_ = p.timeWheel.Cancel(id)
			continue
		}
	}
}

func (p *PingJob) recv(conn net.PacketConn) {
	buf := make([]byte, 1500)
	n, addr, err := conn.ReadFrom(buf)
	if err != nil {
		p.logger.Error("failed to read icmp packet", zap.Error(err))
		return
	}

	ip, ok := netip.AddrFromSlice(addr.(*net.UDPAddr).IP)
	if !ok {
		p.logger.Error("failed to parse source ip from addr", zap.Any("addr", addr))
		return
	}
	proto := ipv4.ICMPTypeEcho.Protocol()
	if ip.Is6() {
		proto = ipv6.ICMPTypeEchoRequest.Protocol()
	}

	msg, err := icmp.ParseMessage(proto, buf[:n])
	if err != nil {
		p.logger.Error("failed to parse icmp message", zap.Error(err))
		return
	}

	if msg.Type != ipv4.ICMPTypeEchoReply && msg.Type != ipv6.ICMPTypeEchoReply {
		p.logger.Warn("received non-echo-reply icmp message", zap.Any("type", msg.Type))
		return
	}

	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		p.logger.Error("failed to cast icmp message body to echo")
		return
	}

	pl, err := unmarshalPayload(echo.Data)
	if err != nil {
		p.logger.Error("failed to unmarshal payload", zap.Error(err))
		return
	}

	err = p.timeWheel.Cancel(pl.ID)
	if err != nil {
		p.logger.Error("failed to cancel timeout timer", zap.Error(err))
		return
	}

	rtt := time.Since(pl.Time)
	p.resultCh <- &pb.AgentMessage{
		Payload: &pb.AgentMessage_ContinuousPingResult{ContinuousPingResult: &pb.ContinuousPingResult{
			JobId:       p.jobID,
			Destination: &pb.IP{Slice: ip.AsSlice()},
			Count:       1,
			Loss:        0,
			RttNano:     []int64{rtt.Nanoseconds()},
		}},
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

func (p *PingJob) newPkt(dst netip.Addr, data []byte) *icmp.Message {
	echo := &icmp.Echo{Seq: rand.Int(), Data: data}

	typ := icmp.Type(ipv4.ICMPTypeEcho)
	if dst.Is6() {
		typ = ipv6.ICMPTypeEchoRequest
	}

	return &icmp.Message{Type: typ, Body: echo}
}
