package main

import (
	"errors"
	"fmt"
	"net"

	"github.com/spf13/cobra"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/DBN-DEV/skyeye/controller"
	"github.com/DBN-DEV/skyeye/pb"
	"github.com/DBN-DEV/skyeye/pkg/log"
	"github.com/DBN-DEV/skyeye/version"
)

type option struct {
	addr string
}

func (o *option) validate() error {
	if o.addr == "" {
		return errors.New("--addr option is required")
	}

	return nil
}

func (o *option) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.addr, "addr", "", "management server address")
}

func (o *option) stratEtcd() error {
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

func (o *option) run() error {
	if err := o.validate(); err != nil {
		return err
	}

	if err := o.stratEtcd(); err != nil {
		return err
	}

	etcdCli, err := clientv3.New(clientv3.Config{Endpoints: []string{"localhost:2379"}})
	if err != nil {
		return fmt.Errorf("create etcd client: %w", err)
	}

	lis, err := net.Listen("tcp", o.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Info("management server listening", zap.String("addr", o.addr))

	grpcServer := grpc.NewServer()
	srv := controller.NewManagementServiceServer(etcdCli)
	pb.RegisterManagementServiceServer(grpcServer, srv)
	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("grpc serve: %w", err)
	}

	return nil
}

func newCmd() *cobra.Command {
	var opt option

	cmd := &cobra.Command{
		Version: version.Version,
		Use:     "skyeye-controller",
		Short:   "Skyeye Controller",
		Long:    "Skyeye Controller manages agents and coordinates probe tasks.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.validate(); err != nil {
				return err
			}

			err := opt.run()
			cobra.CheckErr(err)

			return nil
		},
	}

	opt.addFlags(cmd)

	return cmd
}

func main() {
	version.Println()

	cmd := newCmd()
	if err := cmd.Execute(); err != nil {
		fmt.Println("Error:", err)
	}
}
