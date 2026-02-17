package controller

import (
	"sync"

	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/pkg/log"
	"github.com/DBN-DEV/skyeye/pkg/tsdb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

var _ pb.ManagementServiceServer = (*Srv)(nil)

type Srv struct {
	pb.UnimplementedManagementServiceServer

	etcdCli *clientv3.Client
	writer  tsdb.Writer

	mu     sync.Mutex
	agents map[string]*AgentSession

	logger *zap.Logger
}

func NewManagementServiceServer(etcdCli *clientv3.Client, writer tsdb.Writer) *Srv {
	return &Srv{
		etcdCli: etcdCli,
		writer:  writer,
		agents:  make(map[string]*AgentSession),

		logger: log.With(zap.String("component", "management-service")),
	}
}

func (s *Srv) Stream(srv pb.ManagementService_StreamServer) error {
	s.logger.Info("receive new agent stream connection")

	agentSess := newAgentSession(srv, s.etcdCli, s.writer)

	if err := agentSess.enroll(); err != nil {
		return err
	}

	s.mu.Lock()
	s.agents[agentSess.agentID] = agentSess
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.agents, agentSess.agentID)
		s.mu.Unlock()
	}()

	return agentSess.Run()
}
