package probe

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"golang.org/x/net/icmp"
)

func TestPing_probeOne(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		conn, err := icmp.ListenPacket("udp4", "")
		assert.NoError(t, err)

		p := &Ping{
			dst:     netip.MustParseAddr("127.0.0.1"),
			timeout: time.Second,
			logger:  zap.NewNop(),
		}

		rtt, loss, err := p.probeOne(1, conn)
		assert.NoError(t, err)
		assert.False(t, loss)
		assert.NotEqual(t, time.Duration(0), rtt)
	})

	t.Run("Timeout", func(t *testing.T) {
		conn, err := icmp.ListenPacket("udp4", "")
		assert.NoError(t, err)

		p := &Ping{
			dst:     netip.MustParseAddr("192.0.2.0"), // RFC 5737
			timeout: time.Millisecond,
			logger:  zap.NewNop(),
		}

		rtt, loss, err := p.probeOne(1, conn)
		assert.NoError(t, err)
		assert.True(t, loss)
		assert.Equal(t, time.Duration(0), rtt)
	})
}
