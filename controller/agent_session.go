package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/DBN-DEV/skyeye/controller/kpath"
	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/pkg/log"
)

type AgentSession struct {
	srv pb.ManagementService_StreamServer

	agentID string

	kv clientv3.KV

	logger *zap.Logger
}

func newAgentSession(srv pb.ManagementService_StreamServer, kv clientv3.KV) *AgentSession {
	return &AgentSession{srv: srv, kv: kv, logger: log.L()}
}

func (as *AgentSession) Run() error {
	if err := as.enroll(); err != nil {
		return fmt.Errorf("controller: agent enroll %w", err)
	}

	for {
		msg, err := as.srv.Recv()
		if err != nil {
			return fmt.Errorf("controller: agent receive %w", err)
		}
		switch msg.GetPayload().(type) {
		case *pb.AgentMessage_Heartbeat:
			if err := as.handleHeartbeat(msg.GetHeartbeat()); err != nil {
				return fmt.Errorf("controller: agent handle heartbeat %w", err)
			}
		default:
			return fmt.Errorf("controller: unknown msg type %T", msg)
		}
	}
}

func (as *AgentSession) enroll() error {
	msg, err := as.srv.Recv()
	if err != nil {
		return fmt.Errorf("controller: enroll agent: %w", err)
	}

	registerMsg, ok := msg.GetPayload().(*pb.AgentMessage_Register)
	if !ok {
		return fmt.Errorf("controller: enroll agent: expected register message, got %T", msg.GetPayload())
	}

	as.agentID = registerMsg.Register.GetAgentId()
	as.logger = as.logger.With(zap.String("agent_id", as.agentID))
	as.logger.Info("controller: enroll agent")

	bytes, err := json.Marshal(registerMsg.Register)
	if err != nil {
		return fmt.Errorf("controller: enroll agent: marshal agent attribute: %w", err)
	}

	ctx, cancelFunc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFunc()

	if _, err := as.kv.Put(ctx, kpath.AgentAttr(as.agentID), string(bytes)); err != nil {
		return fmt.Errorf("controller: enroll agent: marshal agent attribute: %w", err)
	}

	return nil
}

func (as *AgentSession) handleHeartbeat(msg *pb.Heartbeat) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	now := time.Now()
	ti := time.UnixMilli(msg.GetTimestampMill())
	diff := ti.Sub(now)
	if diff > time.Minute || diff < -time.Minute {
		as.logger.Info("agent time diff", zap.Duration("diff", diff))
	}

	if _, err := as.kv.Put(ctx, kpath.AgentHeartbeat(as.agentID), now.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("controller: enroll agent: put heartbeat: %w", err)
	}

	return nil
}
