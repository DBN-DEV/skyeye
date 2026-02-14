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

func TestNewContinuousPingTask(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		msg := &pb.ContinuousPingJob{
			JobId:      1,
			IntervalMs: 1000,
			TimeoutMs:  500,
			Destinations: []*pb.IP{
				{Slice: net.IPv4(8, 8, 8, 8).To4()},
				{Slice: net.ParseIP("::1")},
			},
			Source: &pb.ContinuousPingJob_Ip{Ip: &pb.IP{Slice: net.IPv4(192, 168, 1, 1).To4()}},
		}
		resultCh := make(chan *pb.AgentMessage, 10)
		job, err := NewContinuousPingTask(msg, resultCh)
		assert.NoError(t, err)
		assert.Equal(t, 2, len(job.dsts))
		assert.Equal(t, 500*time.Millisecond, job.timeout)
	})

	t.Run("invalid destination", func(t *testing.T) {
		msg := &pb.ContinuousPingJob{
			IntervalMs:   1000,
			TimeoutMs:    500,
			Destinations: []*pb.IP{{Slice: []byte{1, 2, 3}}},
			Source:       &pb.ContinuousPingJob_Ip{Ip: &pb.IP{Slice: net.IPv4(192, 168, 1, 1).To4()}},
		}
		_, err := NewContinuousPingTask(msg, nil)
		assert.Error(t, err)
	})

	t.Run("invalid source", func(t *testing.T) {
		msg := &pb.ContinuousPingJob{
			IntervalMs:   1000,
			TimeoutMs:    500,
			Destinations: []*pb.IP{{Slice: net.IPv4(8, 8, 8, 8).To4()}},
			Source:       &pb.ContinuousPingJob_Ip{Ip: &pb.IP{Slice: []byte{1, 2, 3}}},
		}
		_, err := NewContinuousPingTask(msg, nil)
		assert.Error(t, err)
	})
}

func TestPayload(t *testing.T) {
	ti := time.Unix(0, 1000000)
	id := newTimeWheelID(1, 2, 3)

	pl := &payload{Time: ti, ID: id}
	data, err := pl.marshal()
	assert.NoError(t, err)

	pl2, err := unmarshalPayload(data)
	assert.NoError(t, err)
	assert.Equal(t, pl.Time, pl2.Time)
	assert.Equal(t, pl.ID, pl2.ID)
}

func TestPayload_marshalInvalidID(t *testing.T) {
	pl := &payload{Time: time.Now(), ID: []byte{1, 2, 3}}
	_, err := pl.marshal()
	assert.Error(t, err)
}

func TestUnmarshalPayload_invalidLength(t *testing.T) {
	_, err := unmarshalPayload([]byte{1, 2, 3})
	assert.Error(t, err)
}

func TestParsePkt(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		body := &icmp.Echo{Seq: 1, Data: []byte("testpayload")}
		pkt := &icmp.Message{Type: ipv4.ICMPTypeEchoReply, Code: 0, Body: body}
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		pktData, err := parsePkt(_ipv4Loopback, data)
		assert.NoError(t, err)
		assert.Equal(t, []byte("testpayload"), pktData)
	})

	t.Run("ipv6", func(t *testing.T) {
		body := &icmp.Echo{Seq: 1, Data: []byte("testpayload")}
		pkt := &icmp.Message{Type: ipv6.ICMPTypeEchoReply, Code: 0, Body: body}
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		pktData, err := parsePkt(_ipv6Loopback, data)
		assert.NoError(t, err)
		assert.Equal(t, []byte("testpayload"), pktData)
	})

	t.Run("wrong type", func(t *testing.T) {
		body := &icmp.Echo{Seq: 1, Data: []byte("testpayload")}
		pkt := &icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: body}
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		_, err = parsePkt(_ipv4Loopback, data)
		assert.Error(t, err)
	})
}

func TestTimeoutFn(t *testing.T) {
	resultCh := make(chan *pb.AgentMessage, 1)
	dst := netip.MustParseAddr("8.8.8.8")
	p := &PingJob{
		jobID:   42,
		timeout: 500 * time.Millisecond,
		resultCh: resultCh,
	}

	fn := p.timeoutFn(dst)
	fn()

	msg := <-resultCh
	result := msg.GetContinuousPingResult()
	assert.Equal(t, uint64(42), result.GetJobId())
	assert.Equal(t, dst.AsSlice(), result.GetDestination().GetSlice())
	assert.Equal(t, uint32(1), result.GetCount())
	assert.Equal(t, uint32(1), result.GetLoss())
	assert.Equal(t, []int64{(500 * time.Millisecond).Nanoseconds()}, result.GetRttNano())
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
