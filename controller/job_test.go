package controller

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePingJobConfig(t *testing.T) {
	data := []byte(`{
		"job_id": 1,
		"interval_ms": 1000,
		"timeout_ms": 5000,
		"count": 3,
		"destinations": ["1.1.1.1", "8.8.8.8"],
		"source_port": "eth0"
	}`)

	cfg, err := parsePingJobConfig(data)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), cfg.JobID)
	assert.Equal(t, uint32(1000), cfg.IntervalMs)
	assert.Equal(t, uint32(5000), cfg.TimeoutMs)
	assert.Equal(t, uint32(3), cfg.Count)
	assert.Equal(t, []string{"1.1.1.1", "8.8.8.8"}, cfg.Dests)
	assert.Equal(t, "eth0", cfg.SourcePort)
}

func TestParsePingJobConfig_InvalidJSON(t *testing.T) {
	_, err := parsePingJobConfig([]byte(`{invalid`))
	assert.Error(t, err)
}

func TestPingJobConfigToProto(t *testing.T) {
	cfg := &PingJobConfig{
		JobID:      42,
		IntervalMs: 2000,
		TimeoutMs:  3000,
		Count:      5,
		Dests:      []string{"1.1.1.1", "8.8.8.8"},
		SourcePort: "eth0",
	}

	job, err := cfg.ToProto()
	require.NoError(t, err)

	assert.Equal(t, uint64(42), job.GetJobId())
	assert.Equal(t, uint32(2000), job.GetIntervalMs())
	assert.Equal(t, uint32(3000), job.GetTimeoutMs())
	assert.Equal(t, uint32(5), job.GetCount())
	assert.Len(t, job.GetDestinations(), 2)
	assert.Equal(t, net.ParseIP("1.1.1.1").To4(), net.IP(job.GetDestinations()[0].GetSlice()))
	assert.Equal(t, net.ParseIP("8.8.8.8").To4(), net.IP(job.GetDestinations()[1].GetSlice()))
	assert.Equal(t, "eth0", job.GetPort())
}

func TestPingJobConfigToProto_IPv6(t *testing.T) {
	cfg := &PingJobConfig{
		JobID: 1,
		Dests: []string{"::1"},
	}

	job, err := cfg.ToProto()
	require.NoError(t, err)
	assert.Equal(t, net.ParseIP("::1"), net.IP(job.GetDestinations()[0].GetSlice()))
}

func TestPingJobConfigToProto_NoSourcePort(t *testing.T) {
	cfg := &PingJobConfig{
		JobID: 1,
		Dests: []string{"1.1.1.1"},
	}

	job, err := cfg.ToProto()
	require.NoError(t, err)
	assert.Nil(t, job.GetSource())
}

func TestPingJobConfigToProto_InvalidIP(t *testing.T) {
	cfg := &PingJobConfig{
		JobID: 1,
		Dests: []string{"invalid"},
	}

	_, err := cfg.ToProto()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid IP address")
}
