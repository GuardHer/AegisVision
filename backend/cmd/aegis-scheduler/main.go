package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"aegisvision/backend/internal/config"
	"aegisvision/backend/internal/platform/logger"
	"aegisvision/backend/internal/platform/mediamtx"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/rabbitmq"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/telemetry"
	"aegisvision/backend/internal/scheduler"
)

func main() {
	cfgPath := flag.String("config", "", "Path to YAML configuration file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.New(logger.Options{Service: "aegis-scheduler"}).Error("failed to load configuration", "err", err)
		os.Exit(1)
	}

	log := logger.New(logger.Options{
		Service:    "aegis-scheduler",
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

	cfgQueue, err := rmqClient.DeclareExclusiveBinding(bootCtx, rabbitmq.ExchangeConfig, "")
	if err != nil {
		log.Error("bind scheduler config events queue failed", "err", err)
		os.Exit(1)
	}

	mtxClient := mediamtx.New(cfg.MediaMTX.APIURL, cfg.MediaMTX.RTSPBaseURL, cfg.MediaMTX.WHEPBaseURL, nil)
	rec := scheduler.NewReconciler(scheduler.ReconcilerDeps{
		InstanceID:        cfg.Service.InstanceID,
		State:             scheduler.NewPostgresStateStore(pgStore),
		Redis:             redisClient,
		Bus:               rmqClient,
		MediaMTX:          mtxClient,
		Logger:            log,
		LeaderTTL:         10 * time.Second,
		LeaderRenewEvery:  3 * time.Second,
		ReconcileInterval: time.Duration(cfg.Scheduler.ReconcileIntervalSec) * time.Second,
		LeaseTTL:          time.Duration(cfg.Scheduler.LeaseTTLSeconds) * time.Second,
		NodeTimeout:       time.Duration(cfg.Scheduler.NodeTimeoutSeconds) * time.Second,
	})

	health := telemetry.NewHealth("aegis-scheduler")
	health.AddReadinessCheck("postgres", pgStore.Ping)
	health.AddReadinessCheck("redis", redisClient.Ping)

	healthSrv := &http.Server{
		Addr:              cfg.HTTP.SchedulerAddr,
		Handler:           health.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		health.SetReady(true)
		log.Info("aegis-scheduler health endpoint listening", "addr", cfg.HTTP.SchedulerAddr)
		if serveErr := healthSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	})

	g.Go(func() error {
		<-gCtx.Done()
		health.SetReady(false)
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		return healthSrv.Shutdown(shutCtx)
	})

	g.Go(func() error {
		return rec.Run(gCtx)
	})

	g.Go(func() error {
		err := rmqClient.Consume(gCtx, cfgQueue, 1, func(_ context.Context, _ []byte) error {
			rec.TriggerReconcile()
			return nil
		})
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("aegis-scheduler exited with error", "err", err)
		os.Exit(1)
	}
	log.Info("aegis-scheduler stopped cleanly")
}
