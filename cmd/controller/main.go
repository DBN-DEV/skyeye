package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/spf13/cobra"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/DBN-DEV/skyeye/controller"
	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/pkg/log"
	"github.com/DBN-DEV/skyeye/pkg/tsdb"
	"github.com/DBN-DEV/skyeye/version"
)

type config struct {
	Addr string      `json:"addr"`
	TSDB tsdb.Config `json:"tsdb"`
}

func loadConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	return &cfg, nil
}

func (c *config) validate() error {
	if c.Addr == "" {
		return fmt.Errorf("config: addr is required")
	}

	if c.TSDB.Backend == "" {
		c.TSDB.Backend = "log"
	}

	return nil
}

func startEtcd() error {
	cfg := embed.NewConfig()
	cfg.ZapLoggerBuilder = embed.NewZapLoggerBuilder(log.L())
	cfg.Dir = "default.etcd"

	etcd, err := embed.StartEtcd(cfg)
	if err != nil {
		return fmt.Errorf("start etcd: %w", err)
	}

	select {
	case <-etcd.Server.ReadyNotify():
		log.Info("etcd is ready")
	}

	return nil
}

func run(cfg *config) error {
	if err := startEtcd(); err != nil {
		return err
	}

	etcdCli, err := clientv3.New(clientv3.Config{Endpoints: []string{"localhost:2379"}})
	if err != nil {
		return fmt.Errorf("create etcd client: %w", err)
	}

	writer, err := tsdb.NewWriterFromConfig(cfg.TSDB)
	if err != nil {
		return fmt.Errorf("create tsdb writer: %w", err)
	}
	defer writer.Close()

	lis, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Info("management server listening", zap.String("addr", cfg.Addr))

	grpcServer := grpc.NewServer()
	srv := controller.NewManagementServiceServer(etcdCli, writer)
	pb.RegisterManagementServiceServer(grpcServer, srv)
	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("grpc serve: %w", err)
	}

	return nil
}

func newCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Version: version.Version,
		Use:     "skyeye-controller",
		Short:   "Skyeye Controller",
		Long:    "Skyeye Controller manages agents and coordinates probe tasks.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(configPath)
			if err != nil {
				return err
			}

			if err := cfg.validate(); err != nil {
				return err
			}

			return run(cfg)
		},
	}

	cmd.Flags().StringVar(&configPath, "config", "", "path to JSON config file")
	cmd.MarkFlagRequired("config")

	return cmd
}

func main() {
	version.Println()

	cmd := newCmd()
	if err := cmd.Execute(); err != nil {
		fmt.Println("Error:", err)
	}
}
