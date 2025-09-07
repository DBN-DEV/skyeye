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

		data := marshalData(time.Now(), id)
		pkt := p.newPkt(dst, data)
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

func marshalData(time time.Time, id []byte) []byte {
	data := make([]byte, 0, idLen+8) // 8 bytes for time
	data = append(binary.LittleEndian.AppendUint64(data, uint64(time.UnixNano())), id...)
	return data
}

func (p *PingJob) newPkt(dst netip.Addr, data []byte) *icmp.Message {
	echo := &icmp.Echo{Seq: rand.Int(), Data: data}

	typ := icmp.Type(ipv4.ICMPTypeEcho)
	if dst.Is6() {
		typ = ipv6.ICMPTypeEchoRequest
	}

	return &icmp.Message{Type: typ, Body: echo}
}
