package probe

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/DBN-DEV/skyeye/pb"
)

var (
	_ipv4Loopback = netip.MustParseAddr("127.0.0.1")
	_ipv6Loopback = netip.MustParseAddr("::1")
)

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

func TestParsePkt(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		pkt := newPkt(_ipv4Loopback, []byte("testpayload"))
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		pktData, err := parsePkt(_ipv4Loopback, data)
		assert.NoError(t, err)
		assert.Equal(t, []byte("testpayload"), pktData)
	})

	t.Run("ipv6", func(t *testing.T) {
		pkt := newPkt(_ipv6Loopback, []byte("testpayload"))
		data, err := pkt.Marshal(nil)
		assert.NoError(t, err)

		pktData, err := parsePkt(_ipv6Loopback, data)
		assert.NoError(t, err)
		assert.Equal(t, []byte("testpayload"), pktData)
	})
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

func TestPingJob_recv(t *testing.T) {
	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	assert.NoError(t, err)

	defer conn.Close()

	pkt := newPkt(_ipv4Loopback, []byte("test payload"))
	data, err := pkt.Marshal(nil)
	assert.NoError(t, err)

	_, err = conn.WriteTo(data, &net.UDPAddr{IP: netip.MustParseAddr("1.1.1.1").AsSlice()})
	assert.NoError(t, err)

	resultCh := make(chan *pb.AgentMessage, 1)

	pingJob := &PingJob{
		logger:    zap.NewNop(),
		timeWheel: newTimerWheel(1*time.Second, 60),
		timeout:   2 * time.Second,
		resultCh:  resultCh,
	}

	go pingJob.recv(conn)

	time.Sleep(500 * time.Second)
}
