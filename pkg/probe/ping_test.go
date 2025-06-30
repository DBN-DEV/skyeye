package probe

import (
	"math"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"golang.org/x/net/icmp"
)

func TestIdAlloc(t *testing.T) {
	t.Run("Alloc", func(t *testing.T) {
		alloc := newIdAlloc()
		for i := 0; i < 1000; i++ {
			id, err := alloc.alloc()
			assert.NoError(t, err)
			assert.Equal(t, uint16(i), id)
		}
	})

	t.Run("Free", func(t *testing.T) {
		alloc := newIdAlloc()
		for i := 0; i < math.MaxUint16; i++ {
			id, err := alloc.alloc()
			assert.NoError(t, err)
			assert.Equal(t, uint16(i), id)
		}

		for i := 100; i < 200; i++ {
			alloc.free(uint16(i))
		}

		for i := 100; i < 200; i++ {
			id, err := alloc.alloc()
			assert.NoError(t, err)
			assert.Equal(t, uint16(i), id)
		}
	})
}

func TestPing_probeOne(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		conn, err := icmp.ListenPacket("udp4", "")
		assert.NoError(t, err)

		p := &Ping{
			dst:     netip.MustParseAddr("127.0.0.1"),
			timeout: time.Second,
			idAlloc: newIdAlloc(),
			logger:  zap.NewNop(),
		}

		rtt, loss, err := p.probeOne(1, 1, conn)
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
			idAlloc: newIdAlloc(),
			logger:  zap.NewNop(),
		}

		rtt, loss, err := p.probeOne(1, 1, conn)
		assert.NoError(t, err)
		assert.True(t, loss)
		assert.Equal(t, time.Duration(0), rtt)
	})
}
