package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DBN-DEV/skyeye/controller/kpath"
	"github.com/DBN-DEV/skyeye/pb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

type NetworkInterface struct {
	Name  string   `json:"name"`
	Addrs []string `json:"addrs"`
}

type AgentAttr struct {
	AgentID           string             `json:"agent_id"`
	Version           string             `json:"version"`
	Capabilities      uint64             `json:"capabilities"`
	Hostname          string             `json:"hostname"`
	NetworkInterfaces []NetworkInterface `json:"network_interfaces"`
}

func newAgentAttr(msg *pb.AgentMessage_Register) *AgentAttr {
	ifaces := make([]NetworkInterface, 0, len(msg.Register.GetNetworkInterfaces()))
	for _, ifaceMsg := range msg.Register.GetNetworkInterfaces() {
		iface := NetworkInterface{
			Name:  ifaceMsg.GetName(),
			Addrs: ifaceMsg.GetAddrs(),
		}

		ifaces = append(ifaces, iface)
	}

	return &AgentAttr{
		AgentID:           msg.Register.GetAgentId(),
		Version:           msg.Register.GetVersion(),
		Capabilities:      msg.Register.GetCapabilities(),
		Hostname:          msg.Register.GetHostname(),
		NetworkInterfaces: ifaces,
	}
}

type AgentSession struct {
	srv pb.ManagementService_StreamServer

	agentID string

	kv clientv3.KV

	logger *zap.Logger
}

func newAgentSession(srv pb.ManagementService_StreamServer) *AgentSession {
	return &AgentSession{srv: srv}
}

func (as *AgentSession) Run() {
	go func() {
		if err := as.run(); err != nil {
			as.logger.Error("run in background", zap.Error(err))
		}
	}()
}

func (as *AgentSession) run() error {
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

	attr := newAgentAttr(registerMsg)
	bytes, err := json.Marshal(attr)
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
