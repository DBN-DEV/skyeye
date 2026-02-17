package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"strconv"
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

	etcdCli *clientv3.Client

	sendCh chan *pb.ControllerMessage

	ctx    context.Context
	cancel context.CancelFunc

	logger *zap.Logger
}

func newAgentSession(srv pb.ManagementService_StreamServer, etcdCli *clientv3.Client) *AgentSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &AgentSession{
		srv:     srv,
		etcdCli: etcdCli,
		sendCh:  make(chan *pb.ControllerMessage, 100),
		ctx:     ctx,
		cancel:  cancel,
		logger:  log.L(),
	}
}

func (as *AgentSession) Run() error {
	go as.sendLoop()
	go as.loadAndWatchJobs()

	defer as.cancel()

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
		case *pb.AgentMessage_PingRoundResult:
			if err := as.handlePingRoundResult(msg.GetPingRoundResult()); err != nil {
				return fmt.Errorf("controller: agent handle ping round result %w", err)
			}
		default:
			return fmt.Errorf("controller: unknown msg type %T", msg)
		}
	}
}

func (as *AgentSession) sendLoop() {
	for {
		select {
		case <-as.ctx.Done():
			return
		case msg := <-as.sendCh:
			if err := as.srv.Send(msg); err != nil {
				as.logger.Error("send message to agent", zap.Error(err))
				return
			}
		}
	}
}

func (as *AgentSession) loadAndWatchJobs() {
	prefix := kpath.PingJobPrefix(as.agentID)

	ctx, cancel := context.WithTimeout(as.ctx, 10*time.Second)
	resp, err := as.etcdCli.Get(ctx, prefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		as.logger.Error("load ping jobs from etcd", zap.Error(err))
		return
	}

	for _, kv := range resp.Kvs {
		as.handleJobPut(kv.Key, kv.Value)
	}

	as.logger.Info("loaded existing ping jobs", zap.Int("count", len(resp.Kvs)))

	watchCh := as.etcdCli.Watch(as.ctx, prefix, clientv3.WithPrefix(), clientv3.WithRev(resp.Header.Revision+1))
	for watchResp := range watchCh {
		if watchResp.Err() != nil {
			as.logger.Error("watch ping jobs", zap.Error(watchResp.Err()))
			return
		}
		for _, ev := range watchResp.Events {
			switch ev.Type {
			case clientv3.EventTypePut:
				as.handleJobPut(ev.Kv.Key, ev.Kv.Value)
			case clientv3.EventTypeDelete:
				as.handleJobDelete(ev.Kv.Key)
			}
		}
	}
}

func (as *AgentSession) handleJobPut(key, value []byte) {
	cfg, err := parsePingJobConfig(value)
	if err != nil {
		as.logger.Error("parse ping job config", zap.String("key", string(key)), zap.Error(err))
		return
	}

	job, err := cfg.ToProto()
	if err != nil {
		as.logger.Error("convert ping job config to proto", zap.String("key", string(key)), zap.Error(err))
		return
	}

	msg := &pb.ControllerMessage{
		Payload: &pb.ControllerMessage_PingJob{PingJob: job},
	}

	select {
	case as.sendCh <- msg:
		as.logger.Info("dispatched ping job", zap.Uint64("job_id", cfg.JobID))
	case <-as.ctx.Done():
	}
}

func (as *AgentSession) handleJobDelete(key []byte) {
	jobIDStr := path.Base(string(key))
	jobID, err := strconv.ParseUint(jobIDStr, 10, 64)
	if err != nil {
		as.logger.Error("parse job ID from key", zap.String("key", string(key)), zap.Error(err))
		return
	}

	msg := &pb.ControllerMessage{
		Payload: &pb.ControllerMessage_CancelPingJob{
			CancelPingJob: &pb.CancelPingJob{JobId: jobID},
		},
	}

	select {
	case as.sendCh <- msg:
		as.logger.Info("dispatched cancel ping job", zap.Uint64("job_id", jobID))
	case <-as.ctx.Done():
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

	if _, err := as.etcdCli.Put(ctx, kpath.AgentAttr(as.agentID), string(bytes)); err != nil {
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

	if _, err := as.etcdCli.Put(ctx, kpath.AgentHeartbeat(as.agentID), now.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("controller: enroll agent: put heartbeat: %w", err)
	}

	return nil
}

func (as *AgentSession) handlePingRoundResult(msg *pb.PingRoundResult) error {
	as.logger.Debug(
		"receive ping round result",
		zap.Uint64("job_id", msg.GetJobId()),
		zap.Uint64("round_id", msg.GetRoundId()),
		zap.String("destination", net.IP(msg.GetDestination().GetSlice()).String()),
		zap.Uint32("sent", msg.GetSent()),
		zap.Uint32("recv", msg.GetRecv()),
		zap.Uint32("timeout", msg.GetTimeout()),
		zap.Uint32("send_error", msg.GetSendError()),
		zap.Float64("loss_rate", msg.GetLossRate()),
	)

	return nil
}
