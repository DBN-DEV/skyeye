package probe

import (
	"encoding/binary"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

const idLen = 24 // slotNum 8 bytes, id 8 bytes, offset 8 bytes

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

type timeWheelID []byte

func newTimeWheelID(slotNum int, offset int, timerID int) timeWheelID {
	buf := make([]byte, 8*3)
	binary.LittleEndian.PutUint64(buf[0:8], uint64(slotNum))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(offset))
	binary.LittleEndian.PutUint64(buf[16:24], uint64(timerID))
	return timeWheelID(buf)
}

func (id timeWheelID) SlotNum() int {
	return int(binary.LittleEndian.Uint64(id[0:8]))
}

func (id timeWheelID) Offset() int {
	return int(binary.LittleEndian.Uint64(id[8:16]))
}

func (id timeWheelID) TimerID() int {
	return int(binary.LittleEndian.Uint64(id[16:24]))
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

// Add adds a new timer to the timer wheel and returns the id.
// The delay must be range of (0, tick*(SLOTS-2)], SLOTS is the slots number of time wheel
func (tw *timerWheel) Add(delay time.Duration, callback func()) (timeWheelID, error) {
	if delay <= 0 {
		return nil, errors.New("probe: delay must be greater than zero")
	}

	// step refers to the number of slots after the current one, with a value range of [0, slots-2]
	// slots-1 cannot be selected because the callback in this slot may be executing
	step := int(delay / tw.tick)
	if step > len(tw.slots)-2 {
		return nil, errors.New("probe: delay exceeds timer wheel capacity")
	}

	current := int(tw.current.Load())
	slotNum := (current + step) % len(tw.slots)

	offset, id := tw.slots[slotNum].add(callback)

	return newTimeWheelID(slotNum, offset, id), nil
}

func (tw *timerWheel) Cancel(id timeWheelID) error {
	if len(id) != 24 {
		return errors.New("probe: invalid id length")
	}

	slotNum := id.SlotNum()
	offset := id.Offset()
	timerID := id.TimerID()

	if slotNum < 0 || slotNum >= len(tw.slots) {
		return errors.New("probe: slot number out of range")
	}

	return tw.slots[slotNum].cancel(offset, timerID)
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
			current := tw.current.Load()
			next := current + 1
			if next >= int64(len(tw.slots)) {
				next = 0
			}
			tw.current.Store(next)
			tw.slots[current].execute()
		case <-tw.stop:
			return
		}
	}
}
