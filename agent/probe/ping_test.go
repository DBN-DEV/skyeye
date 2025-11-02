package probe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
