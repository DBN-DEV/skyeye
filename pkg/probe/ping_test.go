package probe

import (
	"github.com/stretchr/testify/assert"
	"math"
	"testing"
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
