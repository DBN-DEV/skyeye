package agent

import (
	"github.com/DBN-DEV/skyeye/agent/probe"
	"github.com/DBN-DEV/skyeye/pb"
	"go.uber.org/zap"
	"sync"
)

type manager struct {
	cli pb.ManagementService_StreamClient

	msgCh chan *pb.AgentMessage

	logger *zap.Logger

	probeMu struct {
		mu     sync.Mutex
		probes map[uint64]probe.ContinuousTask
	}
}

func (m *manager) sendLoop() {
	for msg := range m.msgCh {
		if err := m.cli.Send(msg); err != nil {
			m.logger.Error("send message", zap.Error(err))
			return
		}
	}
}

func (m *manager) recv() {
	for {
		msg, err := m.cli.Recv()
		if err != nil {
			m.logger.Error("recv message", zap.Error(err))
			continue
		}

		m.dispatchCtrlMsg(msg)
	}
}

func (m *manager) dispatchCtrlMsg(msg *pb.ControllerMessage) {
	switch msg.GetPayload().(type) {
	case *pb.ControllerMessage_ContinuousPingTask:
		m.runContinuousPingTask(msg.GetContinuousPingTask())
	default:
		m.logger.Error("invalid ctrl message", zap.Any("msg", msg))
	}
}

func (m *manager) runContinuousPingTask(msg *pb.ContinuousPingTask) {
	p, err := probe.NewContinuousPingTask(msg, m.msgCh)
	if err != nil {
		m.logger.Error("new continuous ping task", zap.Error(err))
		return
	}

	m.probeMu.mu.Lock()
	m.probeMu.probes[msg.GetTaskId()] = p
	m.probeMu.mu.Unlock()

	go p.Run()
}
