package probe

import (
	"errors"
	"github.com/bits-and-blooms/bitset"
	"math"
	"sync"
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
}
