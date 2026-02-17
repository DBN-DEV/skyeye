package kpath

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPingJobPrefix(t *testing.T) {
	assert.Equal(t, "/pingjob/agent-1/", PingJobPrefix("agent-1"))
	assert.Equal(t, "/pingjob/abc-def-123/", PingJobPrefix("abc-def-123"))
}

func TestPingJobPrefix_Panic(t *testing.T) {
	assert.Panics(t, func() { PingJobPrefix("") })
}

func TestPingJobKey(t *testing.T) {
	assert.Equal(t, "/pingjob/agent-1/42", PingJobKey("agent-1", "42"))
	assert.Equal(t, "/pingjob/abc/100", PingJobKey("abc", "100"))
}

func TestPingJobKey_Panic(t *testing.T) {
	assert.Panics(t, func() { PingJobKey("", "1") })
	assert.Panics(t, func() { PingJobKey("agent", "") })
}
