//go:build integration

package probe

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DBN-DEV/skyeye/pb"
)

func TestPingJobIntegration(t *testing.T) {
	sc, err := NewSharedConn()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sc.Run(ctx)

	msg := &pb.PingJob{
		JobId:      1,
		IntervalMs: 2000,
		TimeoutMs:  1000,
		Count:      5,
		Destinations: []*pb.IP{
			{Slice: net.IPv4(127, 0, 0, 1).To4()},
		},
	}

	resultCh := make(chan *pb.AgentMessage, 100)
	job, err := NewPingTask(msg, resultCh, sc)
	require.NoError(t, err)

	go job.Run()

	// Wait for at least 1 round result.
	select {
	case result := <-resultCh:
		r := result.GetPingRoundResult()
		require.NotNil(t, r)

		t.Logf("sent=%d recv=%d timeout=%d loss=%.2f min=%s avg=%s max=%s",
			r.Sent, r.Recv, r.Timeout, r.LossRate,
			time.Duration(r.MinRttNano), time.Duration(r.AvgRttNano), time.Duration(r.MaxRttNano))

		assert.Equal(t, uint32(5), r.Sent)
		assert.Equal(t, uint32(5), r.Recv)
		assert.Equal(t, uint32(0), r.Timeout)
		assert.Equal(t, 0.0, r.LossRate)
		assert.Greater(t, r.AvgRttNano, int64(0))

	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ping round result")
	}

	job.Cancel()
}
