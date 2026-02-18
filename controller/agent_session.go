package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"strconv"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/DBN-DEV/skyeye/controller/kpath"
	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/pkg/log"
	"github.com/DBN-DEV/skyeye/pkg/tsdb"
)

type AgentSession struct {
	srv pb.ManagementService_StreamServer

	agentID string

	etcdCli *clientv3.Client
	writer  tsdb.Writer

	jobTagsMu sync.RWMutex
	jobTags   map[uint64]map[string]string

	sendCh chan *pb.ControllerMessage

	ctx    context.Context
	cancel context.CancelFunc

	logger *zap.Logger
}

func newAgentSession(srv pb.ManagementService_StreamServer, etcdCli *clientv3.Client, writer tsdb.Writer) *AgentSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &AgentSession{
		srv:     srv,
		etcdCli: etcdCli,
		writer:  writer,
		jobTags: make(map[uint64]map[string]string),
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

	as.jobTagsMu.Lock()
	as.jobTags[cfg.JobID] = cfg.Tags
	as.jobTagsMu.Unlock()

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
		return
	}

	go func() {
		select {
		case <-time.After(time.Minute):
		case <-as.ctx.Done():
		}
		as.jobTagsMu.Lock()
		delete(as.jobTags, jobID)
		as.jobTagsMu.Unlock()
	}()
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
	tags := map[string]string{
		"agent_id":    as.agentID,
		"job_id":      strconv.FormatUint(msg.GetJobId(), 10),
		"destination": net.IP(msg.GetDestination().GetSlice()).String(),
	}
	as.jobTagsMu.RLock()
	for k, v := range as.jobTags[msg.GetJobId()] {
		tags[k] = v
	}
	as.jobTagsMu.RUnlock()

	p := tsdb.Point{
		Measurement: "ping",
		Tags:        tags,
		Fields: map[string]any{
			"sent":       msg.GetSent(),
			"recv":       msg.GetRecv(),
			"timeout":    msg.GetTimeout(),
			"send_error": msg.GetSendError(),
			"loss_rate":  msg.GetLossRate(),
		},
		Time: time.Now(),
	}

	if err := as.writer.Write(as.ctx, p); err != nil {
		as.logger.Error("write ping round result", zap.Error(err))
	}

	return nil
}
