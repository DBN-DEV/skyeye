package probe

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/DBN-DEV/skyeye/pb"
)

var (
	_ipv4Loopback = netip.MustParseAddr("127.0.0.1")
	_ipv6Loopback = netip.MustParseAddr("::1")
)

func TestNewPingTask(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		msg := &pb.PingJob{
			JobId:      1,
			IntervalMs: 1000,
			TimeoutMs:  500,
			Count:      3,
			Destinations: []*pb.IP{
				{Slice: net.IPv4(8, 8, 8, 8).To4()},
				{Slice: net.ParseIP("::1")},
			},
			Source: &pb.PingJob_Ip{Ip: &pb.IP{Slice: net.IPv4(192, 168, 1, 1).To4()}},
		}
		resultCh := make(chan *pb.AgentMessage, 10)

		job, err := NewPingTask(msg, resultCh)
		assert.NoError(t, err)
		assert.Equal(t, 1, len(job.dsts4))
		assert.Equal(t, 1, len(job.dsts6))
		assert.Equal(t, 500*time.Millisecond, job.timeout)
		assert.Equal(t, 1000*time.Millisecond, job.interval)
		assert.Equal(t, uint32(3), job.count)
	})

	t.Run("default count", func(t *testing.T) {
		msg := &pb.PingJob{
			JobId:      1,
			IntervalMs: 1000,
			TimeoutMs:  500,
			Destinations: []*pb.IP{
				{Slice: net.IPv4(8, 8, 8, 8).To4()},
			},
			Source: &pb.PingJob_Ip{Ip: &pb.IP{Slice: net.IPv4(192, 168, 1, 1).To4()}},
		}

		job, err := NewPingTask(msg, make(chan *pb.AgentMessage, 1))
		assert.NoError(t, err)
		assert.Equal(t, uint32(1), job.count)
	})

	t.Run("invalid destination", func(t *testing.T) {
		msg := &pb.PingJob{
			IntervalMs:   1000,
			TimeoutMs:    500,
			Destinations: []*pb.IP{{Slice: []byte{1, 2, 3}}},
			Source:       &pb.PingJob_Ip{Ip: &pb.IP{Slice: net.IPv4(192, 168, 1, 1).To4()}},
		}
		_, err := NewPingTask(msg, nil)
		assert.Error(t, err)
	})

	t.Run("invalid source", func(t *testing.T) {
		msg := &pb.PingJob{
			IntervalMs:   1000,
			TimeoutMs:    500,
			Destinations: []*pb.IP{{Slice: net.IPv4(8, 8, 8, 8).To4()}},
			Source:       &pb.PingJob_Ip{Ip: &pb.IP{Slice: []byte{1, 2, 3}}},
		}
		_, err := NewPingTask(msg, nil)
		assert.Error(t, err)
	})
}

func TestPayload(t *testing.T) {
	pl := &payload{
		Time:  time.Unix(0, 1000000),
		Token: 123456789,
	}
	data := pl.marshal()

	pl2, err := unmarshalPayload(data)
	assert.NoError(t, err)
	assert.Equal(t, pl.Time, pl2.Time)
	assert.Equal(t, pl.Token, pl2.Token)
}

func TestUnmarshalPayloadInvalidLength(t *testing.T) {
	_, err := unmarshalPayload([]byte{1, 2, 3})
	assert.Error(t, err)
}

func TestParsePkt(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		body := &icmp.Echo{ID: 1, Seq: 1, Data: []byte("testpayload")}
		pkt := &icmp.Message{Type: ipv4.ICMPTypeEchoReply, Code: 0, Body: body}
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		pktData, err := parsePkt(_ipv4Loopback, data)
		assert.NoError(t, err)
		assert.Equal(t, []byte("testpayload"), pktData)
	})

	t.Run("ipv6", func(t *testing.T) {
		body := &icmp.Echo{ID: 1, Seq: 1, Data: []byte("testpayload")}
		pkt := &icmp.Message{Type: ipv6.ICMPTypeEchoReply, Code: 0, Body: body}
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		pktData, err := parsePkt(_ipv6Loopback, data)
		assert.NoError(t, err)
		assert.Equal(t, []byte("testpayload"), pktData)
	})

	t.Run("wrong type", func(t *testing.T) {
		body := &icmp.Echo{ID: 1, Seq: 1, Data: []byte("testpayload")}
		pkt := &icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: body}
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		_, err = parsePkt(_ipv4Loopback, data)
		assert.Error(t, err)
	})
}

func TestRoundResultTriggerByRecvAndTimeout(t *testing.T) {
	p := &PingJob{
		jobID:    42,
		rounds:   make(map[uint64]map[string]*ipRoundState),
		inFlight: make(map[uint64]*inFlightPacket),
		stopCh:   make(chan struct{}),
		resultCh: make(chan *pb.AgentMessage, 10),
	}

	dst := netip.MustParseAddr("8.8.8.8")
	roundID := uint64(7)

	p.initRoundState(roundID, dst)
	p.recordSent(roundID, dst)
	p.recordSent(roundID, dst)

	msg := p.closeRoundAndBuildResult(roundID, dst)
	assert.Nil(t, msg)

	msg = p.recordRecvAndBuildResult(roundID, dst, 10*time.Millisecond)
	assert.Nil(t, msg)

	msg = p.recordTimeoutAndBuildResult(roundID, dst)
	assert.NotNil(t, msg)

	result := msg.GetPingRoundResult()
	assert.Equal(t, uint64(42), result.GetJobId())
	assert.Equal(t, dst.AsSlice(), result.GetDestination().GetSlice())
	assert.Equal(t, uint32(2), result.GetSent())
	assert.Equal(t, uint32(1), result.GetRecv())
	assert.Equal(t, uint32(1), result.GetTimeout())
	assert.Equal(t, uint32(0), result.GetSendError())
	assert.Equal(t, 0.5, result.GetLossRate())
	assert.Equal(t, (10 * time.Millisecond).Nanoseconds(), result.GetMinRttNano())
	assert.Equal(t, (10 * time.Millisecond).Nanoseconds(), result.GetAvgRttNano())
	assert.Equal(t, (10 * time.Millisecond).Nanoseconds(), result.GetMaxRttNano())
}

func TestSendErrorDoesNotAffectTriggerCondition(t *testing.T) {
	p := &PingJob{
		jobID:    11,
		rounds:   make(map[uint64]map[string]*ipRoundState),
		inFlight: make(map[uint64]*inFlightPacket),
		stopCh:   make(chan struct{}),
		resultCh: make(chan *pb.AgentMessage, 10),
	}

	dst := netip.MustParseAddr("1.1.1.1")
	roundID := uint64(3)

	p.initRoundState(roundID, dst)
	p.recordSent(roundID, dst)
	p.recordSendError(roundID, dst)
	p.recordSendError(roundID, dst)

	msg := p.closeRoundAndBuildResult(roundID, dst)
	assert.Nil(t, msg)

	msg = p.recordTimeoutAndBuildResult(roundID, dst)
	assert.NotNil(t, msg)

	result := msg.GetPingRoundResult()
	assert.Equal(t, uint32(1), result.GetSent())
	assert.Equal(t, uint32(1), result.GetTimeout())
	assert.Equal(t, uint32(2), result.GetSendError())
	assert.Equal(t, 1.0, result.GetLossRate())
}

func TestRoundWithOnlySendErrorsCanSettle(t *testing.T) {
	p := &PingJob{
		jobID:    9,
		rounds:   make(map[uint64]map[string]*ipRoundState),
		inFlight: make(map[uint64]*inFlightPacket),
		stopCh:   make(chan struct{}),
		resultCh: make(chan *pb.AgentMessage, 10),
	}

	dst := netip.MustParseAddr("9.9.9.9")
	roundID := uint64(100)

	p.initRoundState(roundID, dst)
	p.recordSendError(roundID, dst)
	p.recordSendError(roundID, dst)

	msg := p.closeRoundAndBuildResult(roundID, dst)
	assert.NotNil(t, msg)

	result := msg.GetPingRoundResult()
	assert.Equal(t, uint32(0), result.GetSent())
	assert.Equal(t, uint32(0), result.GetRecv())
	assert.Equal(t, uint32(0), result.GetTimeout())
	assert.Equal(t, uint32(2), result.GetSendError())
	assert.Equal(t, 0.0, result.GetLossRate())
	assert.Equal(t, int64(0), result.GetMinRttNano())
	assert.Equal(t, int64(0), result.GetAvgRttNano())
	assert.Equal(t, int64(0), result.GetMaxRttNano())
}

func TestNewPkt(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		pkt := newPkt(_ipv4Loopback, []byte("testpayload"))
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		parsedPkt, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), data)
		assert.NoError(t, err)
		assert.Equal(t, ipv4.ICMPTypeEcho, parsedPkt.Type)
		echo, ok := parsedPkt.Body.(*icmp.Echo)
		assert.True(t, ok)
		assert.Equal(t, []byte("testpayload"), echo.Data)
	})

	t.Run("ipv6", func(t *testing.T) {
		pkt := newPkt(_ipv6Loopback, []byte("testpayload"))
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		parsedPkt, err := icmp.ParseMessage(ipv6.ICMPTypeEchoRequest.Protocol(), data)
		assert.NoError(t, err)
		assert.Equal(t, ipv6.ICMPTypeEchoRequest, parsedPkt.Type)
		echo, ok := parsedPkt.Body.(*icmp.Echo)
		assert.True(t, ok)
		assert.Equal(t, []byte("testpayload"), echo.Data)
	})
}
