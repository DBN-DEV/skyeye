package agent

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/DBN-DEV/skyeye/agent/probe"
	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/version"
)

type Manager struct {
	agentID string

	cli pb.ManagementService_StreamClient

	msgCh chan *pb.AgentMessage

	logger *zap.Logger

	probeMu struct {
		mu     sync.Mutex
		probes map[uint64]probe.ContinuousTask
	}
}

func NewManager(target string) (*Manager, error) {
	cc, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("agent: grpc dial %s: %w", target, err)
	}

	cli := pb.NewManagementServiceClient(cc)
	streamCli, err := cli.Stream(context.Background())
	if err != nil {
		return nil, fmt.Errorf("agent: create stream client: %w", err)
	}

	return &Manager{
		cli:    streamCli,
		msgCh:  make(chan *pb.AgentMessage, 100),
		logger: zap.L().Named("manager"),

		probeMu: struct {
			mu     sync.Mutex
			probes map[uint64]probe.ContinuousTask
		}{
			probes: make(map[uint64]probe.ContinuousTask),
		},
	}, nil
}

func (m *Manager) Run() error {
	if err := m.register(); err != nil {
		return fmt.Errorf("agent: register: %w", err)
	}

	go m.heartbeatLoop()
	go m.sendLoop()
	go m.recvLoop()

	select {}
}

func (m *Manager) sendLoop() {
	for msg := range m.msgCh {
		if err := m.cli.Send(msg); err != nil {
			m.logger.Error("send message", zap.Error(err))
			return
		}
	}
}

func (m *Manager) recvLoop() {
	for {
		msg, err := m.cli.Recv()
		if err != nil {
			m.logger.Error("recv message", zap.Error(err))
			continue
		}

		m.dispatchCtrlMsg(msg)
	}
}

func (m *Manager) dispatchCtrlMsg(msg *pb.ControllerMessage) {
	switch msg.GetPayload().(type) {
	case *pb.ControllerMessage_ContinuousPingJob:
		m.runContinuousPingTask(msg.GetContinuousPingJob())
	case *pb.ControllerMessage_CancelJob:
		m.cancelContinuousTask(msg.GetCancelJob())
	default:
		m.logger.Error("invalid ctrl message", zap.Any("msg", msg))
	}
}

func (m *Manager) runContinuousPingTask(msg *pb.ContinuousPingJob) {
	p, err := probe.NewContinuousPingTask(msg, m.msgCh)
	if err != nil {
		m.logger.Error("new continuous ping task", zap.Error(err))
		return
	}

	m.probeMu.mu.Lock()
	m.probeMu.probes[msg.GetJobId()] = p
	m.probeMu.mu.Unlock()

	go p.Run()
}

func (m *Manager) cancelContinuousTask(msg *pb.CancelContinuousJob) {
	m.probeMu.mu.Lock()
	defer m.probeMu.mu.Unlock()

	jobID := msg.GetJobId()
	if p, ok := m.probeMu.probes[jobID]; ok {
		p.Cancel()
		delete(m.probeMu.probes, jobID)
	}
}

func (m *Manager) register() error {
	iface, err := networkInterfaces()
	if err != nil {
		return fmt.Errorf("agent: get network interfaces: %w", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("agent: get hostname: %w", err)
	}

	msg := &pb.AgentMessage{
		Payload: &pb.AgentMessage_Register{Register: &pb.Register{
			AgentId:           m.agentID,
			Version:           version.Version,
			Hostname:          hostname,
			NetworkInterfaces: iface,
		}},
	}

	if err := m.cli.Send(msg); err != nil {
		return fmt.Errorf("agent: send register message: %w", err)
	}

	return nil
}

func (m *Manager) heartbeatLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := m.heartbeat(); err != nil {
			m.logger.Error("send heartbeat", zap.Error(err))
		}
	}
}

func (m *Manager) heartbeat() error {
	now := time.Now().UnixMilli()

	msg := &pb.AgentMessage{
		Payload: &pb.AgentMessage_Heartbeat{Heartbeat: &pb.Heartbeat{
			TimestampMill: now,
		}},
	}

	if err := m.cli.Send(msg); err != nil {
		return fmt.Errorf("agent: send heartbeat: %w", err)
	}

	return nil
}
