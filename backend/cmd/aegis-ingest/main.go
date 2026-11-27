package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	agentv1 "aegisvision/backend/gen/go/agent/v1"
	"aegisvision/backend/internal/config"
	"aegisvision/backend/internal/ingest"
	"aegisvision/backend/internal/platform/logger"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/rabbitmq"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/storage"
	"aegisvision/backend/internal/rules"
)

func main() {
	cfgPath := flag.String("config", "", "Path to YAML configuration file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.New(logger.Options{Service: "aegis-ingest"}).Error("failed to load configuration", "err", err)
		os.Exit(1)
	}

	log := logger.New(logger.Options{
		Service:    "aegis-ingest",
		InstanceID: cfg.Service.InstanceID,
		Level:      cfg.Log.Level,
		Format:     cfg.Log.Format,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bootCtx, bootCancel := context.WithTimeout(ctx, 20*time.Second)
	defer bootCancel()

	pgStore, err := postgres.New(bootCtx, cfg.Postgres.DSN)
	if err != nil {
		log.Error("connect postgres failed", "dsn", logger.RedactURL(cfg.Postgres.DSN), "err", err)
		os.Exit(1)
	}
	defer pgStore.Close()

	redisClient, err := platformredis.New(bootCtx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Error("connect redis failed", "addr", cfg.Redis.Addr, "err", err)
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()

	rmqClient, err := rabbitmq.New(cfg.RabbitMQ.URL)
	if err != nil {
		log.Error("connect rabbitmq failed", "url", logger.RedactURL(cfg.RabbitMQ.URL), "err", err)
		os.Exit(1)
	}
	defer func() { _ = rmqClient.Close() }()

	if err := rmqClient.EnsureTopology(bootCtx); err != nil {
		log.Error("ensure rabbitmq topology failed", "err", err)
		os.Exit(1)
	}

	objStore, err := storage.New(bootCtx, cfg.Storage)
	if err != nil {
		log.Error("init object storage failed", "driver", cfg.Storage.Driver, "err", err)
		os.Exit(1)
	}

	cmdQueue, err := rmqClient.DeclareExclusiveBinding(
		bootCtx,
		rabbitmq.ExchangeControl,
		rabbitmq.AgentCommandRoutingKey(cfg.Service.InstanceID),
	)
	if err != nil {
		log.Error("bind agent command queue failed", "err", err)
		os.Exit(1)
	}

	cfgQueue, err := rmqClient.DeclareExclusiveBinding(bootCtx, rabbitmq.ExchangeConfig, "")
	if err != nil {
		log.Error("bind config events queue failed", "err", err)
		os.Exit(1)
	}

	repo := ingest.NewPostgresRepo(pgStore)
	ruleEngine := rules.NewEngine()
	if err := repo.ReloadRules(bootCtx, ruleEngine); err != nil {
		log.Warn("initial rule reload failed", "err", err)
	}

	proc := ingest.NewProcessor(ingest.ProcessorDeps{
		Engine:  ruleEngine,
		Repo:    repo,
		Redis:   redisClient,
		MQ:      rmqClient,
		Storage: objStore,
		Logger:  log,
	})
	gw := ingest.NewGateway(ingest.GatewayDeps{
		InstanceID: cfg.Service.InstanceID,
		AgentToken: cfg.Auth.AgentToken,
		Repo:       repo,
		Redis:      redisClient,
		Processor:  proc,
		Logger:     log,
	})

	lis, err := net.Listen("tcp", cfg.GRPC.IngestAddr)
	if err != nil {
		log.Error("listen gRPC failed", "addr", cfg.GRPC.IngestAddr, "err", err)
		os.Exit(1)
	}

	grpcSrv := grpc.NewServer(
		grpc.MaxRecvMsgSize(16 * 1024 * 1024), // allow high-res JPEG snapshots in BufferedEvent
	)
	agentv1.RegisterAgentGatewayServer(grpcSrv, gw)

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.Info("aegis-ingest gRPC listening", "addr", cfg.GRPC.IngestAddr)
		return grpcSrv.Serve(lis)
	})

	g.Go(func() error {
		<-gCtx.Done()
		done := make(chan struct{})
		go func() {
			grpcSrv.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			grpcSrv.Stop()
		}
		return nil
	})

	g.Go(func() error {
		err := rmqClient.Consume(gCtx, cmdQueue, 16, gw.HandleRoutedCommand)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	})

	g.Go(func() error {
		err := rmqClient.Consume(gCtx, cfgQueue, 1, func(c context.Context, _ []byte) error {
			if reloadErr := repo.ReloadRules(c, ruleEngine); reloadErr != nil {
				log.Warn("hot-reload rules failed", "err", reloadErr)
			}
			return nil
		})
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("aegis-ingest exited with error", "err", err)
		os.Exit(1)
	}
	log.Info("aegis-ingest stopped cleanly")
}
