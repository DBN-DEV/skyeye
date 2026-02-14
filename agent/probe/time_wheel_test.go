package probe

import (
	"encoding/binary"
	"testing"
	"testing/synctest"
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

func TestSlot_execute(t *testing.T) {
	s := &slot{}
	var executed bool
	callback := func() { executed = true }
	s.add(callback)

	// Execute should call the callback
	s.execute()
	assert.True(t, executed)

	// Add another timer and cancel it
	executed = false
	offset, id := s.add(callback)
	err := s.cancel(offset, id)
	assert.NoError(t, err)

	// Execute should not call the cancelled callback
	s.execute()
	assert.False(t, executed)
}

func TestTimerWheel_Add(t *testing.T) {
	type TestCase struct {
		name             string
		twSlots          int
		twCurrentSlot    int
		twTick           time.Duration
		paramsDelay      time.Duration
		expectedSlotNum  int
		expectedOffset   int
		expectedError    bool
		expectedErrorMsg string
	}
	tcs := []TestCase{
		{
			name:             "Add with negative delay",
			twSlots:          10,
			twCurrentSlot:    0,
			twTick:           time.Millisecond,
			paramsDelay:      -time.Millisecond,
			expectedError:    true,
			expectedErrorMsg: "probe: delay must be greater than zero",
		},
		{
			name:             "Add with zero delay",
			twSlots:          10,
			twCurrentSlot:    0,
			twTick:           time.Millisecond,
			paramsDelay:      0,
			expectedError:    true,
			expectedErrorMsg: "probe: delay must be greater than zero",
		},
		{
			name:             "Add with delay exceeds timer wheel capacity",
			twSlots:          10,
			twCurrentSlot:    0,
			twTick:           time.Millisecond,
			paramsDelay:      9 * time.Millisecond,
			expectedSlotNum:  0,
			expectedOffset:   0,
			expectedError:    true,
			expectedErrorMsg: "probe: delay exceeds timer wheel capacity",
		},
		{
			name:            "Add with up bound valid delay",
			twSlots:         10,
			twCurrentSlot:   0,
			twTick:          time.Millisecond,
			paramsDelay:     9*time.Millisecond - time.Nanosecond,
			expectedSlotNum: 8,
			expectedOffset:  0,
			expectedError:   false,
		},
		{
			name:            "Add with down bound valid delay",
			twSlots:         10,
			twCurrentSlot:   0,
			twTick:          time.Millisecond,
			paramsDelay:     time.Nanosecond,
			expectedSlotNum: 0,
			expectedOffset:  0,
			expectedError:   false,
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			tw := &timerWheel{
				tick:  tc.twTick,
				slots: make([]slot, tc.twSlots),
			}
			tw.current.Store(int64(tc.twCurrentSlot))

			id, err := tw.Add(tc.paramsDelay, func() {})

			if tc.expectedError {
				if err == nil {
					t.Fatal("expected error, but got nil")
				}
				assert.Equal(t, tc.expectedErrorMsg, err.Error())
			} else {
				if err != nil {
					t.Fatal("expected no error, but got:", err)
				}
				assert.Len(t, id, 24) // 8 bytes for slotNum, 8bytes for id, 8 bytes for offset
				assert.Equal(t, tc.expectedSlotNum, id.SlotNum())
				assert.Equal(t, tc.expectedOffset, id.Offset())
			}
		})
	}
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
	binary.LittleEndian.PutUint64(id[0:8], 10) // Invalid slot number
	err = tw.Cancel(id)
	assert.Error(t, err)

	// Cancel with valid id but no timer exists
	binary.LittleEndian.PutUint64(id[0:8], 0)   // Valid slot number
	binary.LittleEndian.PutUint64(id[8:16], 0)  // Valid offset
	binary.LittleEndian.PutUint64(id[16:24], 1) // Non-existing timer ID
	err = tw.Cancel(id)
	assert.Error(t, err)

	// Add a timer and then cancel it
	buf, err := tw.Add(5*time.Millisecond, func() {})
	assert.NoError(t, err)
	err = tw.Cancel(buf)
	assert.NoError(t, err)
	assert.True(t, tw.slots[5].timers[0].cancelled)
}

func TestTimerWheel_run(t *testing.T) {
	type TestCase struct {
		name     string
		twSlots  int
		twTick   time.Duration
		jobSlot  int
		sleep    time.Duration
		executed bool
	}
	tcs := []TestCase{
		{
			name:     "job will executed",
			twSlots:  10,
			twTick:   time.Second,
			jobSlot:  5,
			sleep:    10 * time.Second,
			executed: true,
		},
		{
			name:     "job will not executed",
			twSlots:  10,
			twTick:   time.Second,
			jobSlot:  5,
			sleep:    1 * time.Second,
			executed: false,
		},
		{
			name:     "job will executed just now",
			twSlots:  10,
			twTick:   time.Second,
			jobSlot:  1,
			sleep:    2*time.Second + time.Millisecond,
			executed: true,
		},
		{
			name:     "Job almost executed",
			twSlots:  10,
			twTick:   time.Second,
			jobSlot:  1,
			sleep:    2*time.Second - time.Millisecond,
			executed: false,
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tw := newTimerWheel(tc.twTick, tc.twSlots)
				var executed bool
				callback := func() { executed = true }
				tw.slots[tc.jobSlot].add(callback)
				time.Sleep(tc.sleep)
				tw.Stop()

				if executed != tc.executed {
					t.Fatalf("expected executed: %v, but got: %v", tc.executed, executed)
				}
			})
		})
	}
}
