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

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/config"
	"aegisvision/backend/internal/evidence"
	"aegisvision/backend/internal/notifier"
	"aegisvision/backend/internal/platform/logger"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/rabbitmq"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/storage"
	"aegisvision/backend/internal/platform/telemetry"
)

func main() {
	cfgPath := flag.String("config", "", "Path to YAML configuration file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.New(logger.Options{Service: "aegis-dispatcher"}).Error("failed to load configuration", "err", err)
		os.Exit(1)
	}

	log := logger.New(logger.Options{
		Service:    "aegis-dispatcher",
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

	redisClient, err := platformredis.New(bootCtx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Error("connect redis failed", "addr", cfg.Redis.Addr, "err", err)
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()

	registry := notifier.NewRegistry()
	registry.Register(notifier.NewWebhookNotifier(nil))

	signer := auth.NewEvidenceSigner(cfg.Auth.EvidenceSignSecret)
	disp := notifier.NewDispatcher(notifier.DispatcherDeps{
		Registry:      registry,
		Store:         notifier.NewPostgresDeliveryStore(pgStore),
		RetryBus:      rmqClient,
		Signer:        signer,
		PublicBaseURL: cfg.HTTP.PublicBaseURL,
		EvidenceTTL:   cfg.Auth.EvidenceURLTTL,
		Logger:        log,
	})

	recorder := evidence.NewRecorder(evidence.RecorderDeps{
		Repo:        evidence.NewPostgresVideoRepo(pgStore),
		Storage:     objStore,
		Tokens:      redisClient,
		RTSPBaseURL: cfg.MediaMTX.RTSPBaseURL,
		Logger:      log,
	})

	health := telemetry.NewHealth("aegis-dispatcher")
	health.AddReadinessCheck("postgres", pgStore.Ping)
	health.AddReadinessCheck("redis", redisClient.Ping)

	healthSrv := &http.Server{
		Addr:              cfg.HTTP.DispatcherAddr,
		Handler:           health.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		health.SetReady(true)
		log.Info("aegis-dispatcher health endpoint listening", "addr", cfg.HTTP.DispatcherAddr)
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

	// Competing consumer #1: alarm notification delivery with DLX backoff retries
	g.Go(func() error {
		err := rmqClient.Consume(gCtx, rabbitmq.QueueAlarmsTriggered, 8, disp.HandleMessage)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	})

	// Competing consumer #2: asynchronous MP4 video evidence recording (never blocks alarm delivery)
	g.Go(func() error {
		err := rmqClient.Consume(gCtx, rabbitmq.QueueEvidenceRecord, 2, func(c context.Context, body []byte) error {
			if recErr := recorder.HandleMessage(c, body); recErr != nil {
				log.Warn("async evidence video recording failed", "err", recErr)
			}
			// Always ACK after recorder marks video_status=failed so bad streams never poison the queue.
			return nil
		})
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("aegis-dispatcher exited with error", "err", err)
		os.Exit(1)
	}
	log.Info("aegis-dispatcher stopped cleanly")
}
