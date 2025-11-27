package controller

import (
	"sync"

	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/pkg/log"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

var _ pb.ManagementServiceServer = (*Srv)(nil)

type Srv struct {
	pb.UnimplementedManagementServiceServer

	etcdCli *clientv3.Client

	mu     sync.Mutex
	agents map[string]*AgentSession

	logger *zap.Logger
}

func NewManagementServiceServer(etcdCli *clientv3.Client) *Srv {
	return &Srv{
		etcdCli: etcdCli,
		agents:  make(map[string]*AgentSession),

		logger: log.With(zap.String("component", "management-service")),
	}
}

func (s *Srv) Stream(srv pb.ManagementService_StreamServer) error {
	s.logger.Info("receive new agent stream connection")

	agentSess := newAgentSession(srv, s.etcdCli)

	return agentSess.Run()
}
