package probe

import (
	"encoding/binary"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

type timer struct {
	id        int
	cancelled bool
	callback  func()
}

type slot struct {
	mu     sync.Mutex
	timers []timer
}

// add adds a new timer to the slot and returns offset and id.
func (s *slot) add(callback func()) (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := timer{id: rand.Int(), callback: callback}
	s.timers = append(s.timers, t)
	return len(s.timers) - 1, t.id
}

func (s *slot) cancel(offset int, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if offset < 0 || offset >= len(s.timers) {
		return errors.New("probe: offset out of range")
	}

	if s.timers[offset].id != id {
		return errors.New("probe: timer ID does not match")
	}

	s.timers[offset].cancelled = true
	return nil
}

func (s *slot) execute() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.timers {
		if s.timers[i].cancelled {
			continue
		}

		s.timers[i].callback()
	}

	s.timers = s.timers[:0]
}

type timerWheel struct {
	ticker *time.Ticker
	stop   chan struct{}

	tick    time.Duration
	current atomic.Int64
	slots   []slot
}

func newTimerWheel(tick time.Duration, slotNum int) *timerWheel {
	tw := &timerWheel{
		ticker: time.NewTicker(tick),
		tick:   tick,
		slots:  make([]slot, slotNum),
		stop:   make(chan struct{}),
	}

	go tw.run()
	return tw
}

func (tw *timerWheel) Add(delay time.Duration, callback func()) ([]byte, error) {
	if delay <= 0 {
		return nil, errors.New("probe: delay must be greater than zero")
	}

	step := int(delay / tw.tick)
	if step > len(tw.slots) {
		return nil, errors.New("probe: delay exceeds timer wheel capacity")
	}

	current := tw.current.Load()
	slotNum := int(current + int64(delay/tw.tick)%int64(len(tw.slots)))

	offset, id := tw.slots[slotNum].add(callback)

	// slotNum 8 bytes, id 8 bytes, offset 8 bytes
	buf := make([]byte, 8*3)
	binary.LittleEndian.PutUint64(buf[0:8], uint64(slotNum))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(offset))
	binary.LittleEndian.PutUint64(buf[16:24], uint64(id))

	return buf, nil
}

func (tw *timerWheel) Cancel(id []byte) error {
	if len(id) != 24 {
		return errors.New("probe: invalid id length")
	}

	slotNum := binary.LittleEndian.Uint64(id[0:8])
	offset := binary.LittleEndian.Uint64(id[8:16])
	timerID := binary.LittleEndian.Uint64(id[16:24])

	if int(slotNum) < 0 || int(slotNum) >= len(tw.slots) {
		return errors.New("probe: slot number out of range")
	}

	return tw.slots[slotNum].cancel(int(offset), int(timerID))
}

func (tw *timerWheel) Stop() {
	if tw.ticker != nil {
		tw.ticker.Stop()
	}
	close(tw.stop)
}

func (tw *timerWheel) run() {
	for {
		select {
		case <-tw.ticker.C:
			next := tw.current.Add(1)
			slotNum := tw.current.Load() % int64(len(tw.slots))
			tw.slots[slotNum].execute()
			if next >= int64(len(tw.slots)) {
				tw.current.Store(0) // Reset current to 0 if it exceeds the number of slots
			}
		case <-tw.stop:
			return
		}
	}
}
