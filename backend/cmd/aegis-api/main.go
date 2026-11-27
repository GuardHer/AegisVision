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

	"github.com/gin-gonic/gin"

	"aegisvision/backend/internal/api"
	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/config"
	"aegisvision/backend/internal/platform/logger"
	"aegisvision/backend/internal/platform/mediamtx"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/rabbitmq"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/storage"
	"aegisvision/backend/internal/platform/telemetry"
)

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "migrate" && os.Args[2] == "up" {
		runMigrateSubcommand(os.Args[3:])
		return
	}

	cfgPath := flag.String("config", "", "Path to YAML configuration file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log := logger.New(logger.Options{Service: "aegis-api"})
		log.Error("failed to load configuration", "err", err)
		os.Exit(1)
	}

	log := logger.New(logger.Options{
		Service:    "aegis-api",
		InstanceID: cfg.Service.InstanceID,
		Level:      cfg.Log.Level,
		Format:     cfg.Log.Format,
	})

	if cfg.Service.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bootCtx, bootCancel := context.WithTimeout(ctx, 20*time.Second)
	defer bootCancel()

	if os.Getenv("AEGIS_AUTO_MIGRATE") == "true" {
		if err := postgres.RunMigrations(bootCtx, cfg.Postgres.DSN); err != nil {
			log.Error("auto migration failed", "err", err)
			os.Exit(1)
		}
	}

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

	initAdminPass := os.Getenv("AEGIS_INIT_ADMIN_PASSWORD")
	if initAdminPass == "" {
		initAdminPass = "Admin@123456"
	}
	if created, err := api.BootstrapDefaultAdmin(bootCtx, pgStore, "admin", initAdminPass); err != nil {
		log.Error("bootstrap default admin failed", "err", err)
		os.Exit(1)
	} else if created {
		log.Warn("bootstrapped default admin account; change password immediately", "username", "admin")
	}

	mtxClient := mediamtx.New(cfg.MediaMTX.APIURL, cfg.MediaMTX.RTSPBaseURL, cfg.MediaMTX.WHEPBaseURL, nil)
	tokenMgr := auth.NewTokenManager(cfg.Auth.JWTSecret, cfg.Auth.AccessTokenTTL)
	signer := auth.NewEvidenceSigner(cfg.Auth.EvidenceSignSecret)

	health := telemetry.NewHealth("aegis-api")
	health.AddReadinessCheck("postgres", pgStore.Ping)
	health.AddReadinessCheck("redis", redisClient.Ping)

	router := api.NewRouter(api.RouterDeps{
		Store:           pgStore,
		Cache:           redisClient,
		Publisher:       rmqClient,
		Storage:         objStore,
		MediaMTX:        mtxClient,
		Tokens:          tokenMgr,
		Signer:          signer,
		Health:          health,
		Logger:          log,
		RefreshTokenTTL: cfg.Auth.RefreshTokenTTL,
	})

	srv := &http.Server{
		Addr:              cfg.HTTP.APIAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	health.SetReady(true)
	log.Info("aegis-api listening", "addr", cfg.HTTP.APIAddr)

	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received; draining traffic")
	case serveErr := <-errCh:
		if serveErr != nil {
			log.Error("http server stopped with error", "err", serveErr)
			os.Exit(1)
		}
	}

	// Graceful shutdown within 10s: flip /readyz -> drain HTTP -> deferred close RMQ/Redis/PG
	health.SetReady(false)
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("graceful http shutdown failed", "err", err)
	}
	log.Info("aegis-api stopped cleanly")
}

func runMigrateSubcommand(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	cfgPath := fs.String("config", "", "Path to YAML configuration file")
	_ = fs.Parse(args)

	log := logger.New(logger.Options{Service: "aegis-api-migrate"})
	dsn := os.Getenv("AEGIS_POSTGRES_DSN")
	if dsn == "" {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			log.Error("load config for migrate failed", "err", err)
			os.Exit(1)
		}
		dsn = cfg.Postgres.DSN
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := postgres.RunMigrations(ctx, dsn); err != nil {
		log.Error("database migration failed", "err", err)
		os.Exit(1)
	}
	log.Info("database migrations applied successfully")
}
