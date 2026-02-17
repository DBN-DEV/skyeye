package controller

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/DBN-DEV/skyeye/pb"
)

type PingJobConfig struct {
	JobID      uint64            `json:"job_id"`
	IntervalMs uint32            `json:"interval_ms"`
	TimeoutMs  uint32            `json:"timeout_ms"`
	Count      uint32            `json:"count"`
	Dests      []string          `json:"destinations"`
	SourcePort string            `json:"source_port"`
	Tags       map[string]string `json:"tags,omitempty"`
}

func (c *PingJobConfig) ToProto() (*pb.PingJob, error) {
	dests := make([]*pb.IP, 0, len(c.Dests))
	for _, d := range c.Dests {
		ip := net.ParseIP(d)
		if ip == nil {
			return nil, fmt.Errorf("invalid IP address: %s", d)
		}
		ip4 := ip.To4()
		if ip4 != nil {
			ip = ip4
		}
		dests = append(dests, &pb.IP{Slice: ip})
	}

	job := &pb.PingJob{
		JobId:        c.JobID,
		IntervalMs:   c.IntervalMs,
		TimeoutMs:    c.TimeoutMs,
		Count:        c.Count,
		Destinations: dests,
	}

	if c.SourcePort != "" {
		job.Source = &pb.PingJob_Port{Port: c.SourcePort}
	}

	return job, nil
}

func parsePingJobConfig(data []byte) (*PingJobConfig, error) {
	var cfg PingJobConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal ping job config: %w", err)
	}
	return &cfg, nil
}
