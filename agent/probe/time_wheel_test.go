package probe

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSlot_add(t *testing.T) {
	s := &slot{}
	callback := func() {}
	offset, id := s.add(callback)

	assert.Equal(t, 0, offset)
	assert.Equal(t, id, s.timers[0].id)
}

func TestSlot_cancel(t *testing.T) {
	s := &slot{}
	callback := func() {}
	offset, id := s.add(callback)

	// Cancel with invalid offset
	err := s.cancel(-1, id)
	assert.Error(t, err)

	// Cancel with mismatched id
	err = s.cancel(offset, id+1)
	assert.Error(t, err)

	// Cancel with valid offset and id
	err = s.cancel(offset, id)
	assert.NoError(t, err)
	assert.True(t, s.timers[offset].cancelled)
}

func TestTimerWheel_Add(t *testing.T) {
	tw := &timerWheel{
		tick:  time.Millisecond,
		slots: make([]slot, 10),
	}

	// delay <= 0
	_, err := tw.Add(0, func() {})
	assert.Error(t, err)
	_, err = tw.Add(-time.Millisecond, func() {})
	assert.Error(t, err)

	// delay exceeds timer wheel capacity
	_, err = tw.Add(11*time.Millisecond, func() {})
	assert.Error(t, err)

	// valid delay
	buf, err := tw.Add(5*time.Millisecond, func() {})
	assert.NoError(t, err)
	assert.Len(t, buf, 24)                      // 8 bytes for slotNum, 8bytes for id, 8 bytes for offset
	assert.Equal(t, 1, len(tw.slots[5].timers)) // One timer should be added to slot 5
	// slotNum should be 5, offset should be 0, id should eq to the timer's id
	assert.Equal(t, uint64(5), binary.BigEndian.Uint64(buf[0:8]))
	assert.Equal(t, uint64(0), binary.BigEndian.Uint64(buf[8:16]))
	assert.Equal(t, uint64(tw.slots[5].timers[0].id), binary.BigEndian.Uint64(buf[16:24]))
}

func TestTimerWheel_Cancel(t *testing.T) {
	tw := &timerWheel{
		tick:  time.Millisecond,
		slots: make([]slot, 10),
	}

	// Cancel with invalid id length
	err := tw.Cancel([]byte{0, 1, 2})
	assert.Error(t, err)

	// Cancel with invalid slot number
	id := make([]byte, 24)
	binary.BigEndian.PutUint64(id[0:8], 10) // Invalid slot number
	err = tw.Cancel(id)
	assert.Error(t, err)

	// Cancel with valid id but no timer exists
	binary.BigEndian.PutUint64(id[0:8], 0)   // Valid slot number
	binary.BigEndian.PutUint64(id[8:16], 0)  // Valid offset
	binary.BigEndian.PutUint64(id[16:24], 1) // Non-existing timer ID
	err = tw.Cancel(id)
	assert.Error(t, err)

	// Add a timer and then cancel it
	buf, err := tw.Add(5*time.Millisecond, func() {})
	assert.NoError(t, err)
	err = tw.Cancel(buf)
	assert.NoError(t, err)
	assert.True(t, tw.slots[5].timers[0].cancelled)
}
