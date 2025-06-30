package probe

import (
	"errors"
	"fmt"
	"github.com/DBN-DEV/skyeye/pb"
	"math"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/bits-and-blooms/bitset"
)

type idAlloc struct {
	mu sync.Mutex

	lastId uint16
	idSet  *bitset.BitSet
}

func newIdAlloc() *idAlloc {
	return &idAlloc{idSet: bitset.New(uint(math.MaxUint16))}
}

func (alloc *idAlloc) alloc() (uint16, error) {
	alloc.mu.Lock()
	defer alloc.mu.Unlock()

	// First try to find an ID starting from lastId
	id, ok := alloc.idSet.NextClear(uint(alloc.lastId))
	if !ok {
		// If not found, try from the beginning (0)
		id, ok = alloc.idSet.NextClear(0)
		if !ok {
			return 0, errors.New("all ids are used")
		}
	}

	alloc.lastId = uint16(id)
	alloc.idSet.Set(id)
	return uint16(id), nil
}

func (alloc *idAlloc) free(id uint16) {
	alloc.mu.Lock()
	defer alloc.mu.Unlock()

	alloc.idSet.Clear(uint(id))
}

type Ping struct {
	idAlloc *idAlloc

	taskID uint64

	network string
	src     string
	dst     netip.Addr

	count    uint32
	timeout  time.Duration
	interval time.Duration

	resultCh chan *pb.AgentMessage
	stopCh   chan struct{}

	logger *zap.Logger
}

type pingResult struct {
	loss    uint32
	rttNano []int64
}

func (p *Ping) Cancel() { close(p.stopCh) }

func (p *Ping) runLoop() {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.probeAndSend()
		case <-p.stopCh:
			return
		}
	}
}

func (p *Ping) probeAndSend() {
	result, err := p.probe()
	if err != nil {
		p.logger.Error("ping: probe", zap.Error(err))
		return
	}

	msg := &pb.AgentMessage{
		Payload: &pb.AgentMessage_ContinuousPingResult{
			ContinuousPingResult: &pb.ContinuousPingResult{
				TaskId:  p.taskID,
				Count:   p.count,
				Loss:    result.loss,
				RttNano: result.rttNano,
			},
		},
	}

	p.resultCh <- msg
}

func (p *Ping) probe() (pingResult, error) {
	conn, err := icmp.ListenPacket(p.network, p.src)
	if err != nil {
		return pingResult{}, fmt.Errorf("ping: listen packet: %w", err)
	}

	id, err := p.idAlloc.alloc()
	if err != nil {
		return pingResult{}, fmt.Errorf("ping: alloc id: %w", err)
	}
	defer p.idAlloc.free(id)

	var result pingResult
	for i := uint32(1); i <= p.count; i++ {
		rtt, loss, err := p.probeOne(id, uint16(i), conn)
		if err != nil {
			return pingResult{}, fmt.Errorf("ping: probe one: %w", err)
		}
		if loss {
			p.logger.Debug("ping loss", zap.Uint32("seq", i))
			result.loss += 1
		} else {
			p.logger.Debug("ping result", zap.Uint32("seq", i), zap.Duration("rtt", rtt))
			result.rttNano = append(result.rttNano, rtt.Nanoseconds())
		}
	}

	return result, nil
}

func (p *Ping) probeOne(id, seq uint16, conn net.PacketConn) (time.Duration, bool, error) {
	pkt := p.newPkt(id, seq)
	byts, err := pkt.Marshal(nil)
	if err != nil {
		return 0, false, fmt.Errorf("ping: marshal packet: %w", err)
	}

	sendAt := time.Now()
	_, err = conn.WriteTo(byts, &net.UDPAddr{IP: p.dst.AsSlice()})
	if err != nil {
		return 0, false, fmt.Errorf("ping: write to: %w", err)
	}

	deadline := sendAt.Add(p.timeout)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return 0, false, fmt.Errorf("ping: set read deadline: %w", err)
	}

	for {
		_, peer, err := conn.ReadFrom(nil)
		if err != nil && errors.Is(err, os.ErrDeadlineExceeded) {
			return 0, true, nil
		}
		if err != nil {
			return 0, false, fmt.Errorf("ping: read from: %w", err)
		}

		if peer.String() != p.dst.String()+":0" {
			p.logger.Debug("unexpected peer", zap.String("peer", peer.String()))
			continue
		}

		return time.Since(sendAt), false, nil
	}
}

func (p *Ping) newPkt(id, seq uint16) *icmp.Message {
	echo := &icmp.Echo{
		ID:   int(id),
		Seq:  int(seq),
		Data: []byte("123456789"),
	}

	typ := icmp.Type(ipv4.ICMPTypeEcho)
	if p.dst.Is6() {
		typ = ipv6.ICMPTypeEchoRequest
	}

	return &icmp.Message{Type: typ, Body: echo}
}
