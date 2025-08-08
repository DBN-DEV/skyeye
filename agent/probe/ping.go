package probe

import (
	"fmt"
	"net/netip"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/pkg/log"
)

type PingJob struct {
	jobID uint64

	src  string
	dsts []netip.Addr

	timeout  time.Duration
	interval time.Duration
	count    uint32

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

	return &PingJob{
		src:      src.String(), // can not support port for now
		dsts:     dests,
		count:    msg.GetCount(),
		timeout:  time.Duration(msg.GetTimeoutMs()) * time.Millisecond,
		interval: time.Duration(msg.GetIntervalMs()) * time.Millisecond,
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
