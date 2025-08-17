package probe

import (
	"fmt"
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

	logger *zap.Logger
}

func NewContinuousPingTask(msg *pb.ContinuousPingJob, resultCh chan<- *pb.AgentMessage) (*PingJob, error) {
	dests := make([]netip.Addr, 0, len(msg.Destinations))
	for _, addr := range msg.GetDestinations() {
		dest, ok := netip.AddrFromSlice(addr.Slice)
		if !ok {
			return nil, fmt.Errorf("ping: invalid destination address %s", addr.String())
		}
		dests = append(dests, dest)
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

func (p *PingJob) newPkt(dst netip.Addr, seq uint16, data []byte) *icmp.Message {
	echo := &icmp.Echo{Seq: int(seq), Data: data}

	typ := icmp.Type(ipv4.ICMPTypeEcho)
	if dst.Is6() {
		typ = ipv6.ICMPTypeEchoRequest
	}

	return &icmp.Message{Type: typ, Body: echo}
}
