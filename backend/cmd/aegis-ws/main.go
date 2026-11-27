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
	"aegisvision/backend/internal/platform/logger"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/telemetry"
	"aegisvision/backend/internal/ws"
)

func main() {
	cfgPath := flag.String("config", "", "Path to YAML configuration file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.New(logger.Options{Service: "aegis-ws"}).Error("failed to load configuration", "err", err)
		os.Exit(1)
	}

	log := logger.New(logger.Options{
		Service:    "aegis-ws",
		InstanceID: cfg.Service.InstanceID,
		Level:      cfg.Log.Level,
		Format:     cfg.Log.Format,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bootCtx, bootCancel := context.WithTimeout(ctx, 20*time.Second)
	defer bootCancel()

	redisClient, err := platformredis.New(bootCtx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Error("connect redis failed", "addr", cfg.Redis.Addr, "err", err)
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()

	hub := ws.NewHub(ws.HubConfig{
		FrameBufferSize: 16,
		AlarmBufferSize: 64,
	})
	tokenMgr := auth.NewTokenManager(cfg.Auth.JWTSecret, cfg.Auth.AccessTokenTTL)
	health := telemetry.NewHealth("aegis-ws")
	health.AddReadinessCheck("redis", redisClient.Ping)

	wsSrv := ws.NewServer(ws.ServerDeps{
		Hub:       hub,
		Tokens:    tokenMgr,
		Blacklist: redisClient,
		Health:    health,
		Logger:    log,
	})

	httpSrv := &http.Server{
		Addr:              cfg.HTTP.WSAddr,
		Handler:           wsSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	framesCh, unsubFrames, err := redisClient.SubscribeFrames(ctx)
	if err != nil {
		log.Error("subscribe redis frames failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = unsubFrames() }()

	alarmsCh, unsubAlarms, err := redisClient.SubscribeRealtimeAlarms(ctx)
	if err != nil {
		log.Error("subscribe redis realtime alarms failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = unsubAlarms() }()

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		health.SetReady(true)
		log.Info("aegis-ws listening", "addr", cfg.HTTP.WSAddr)
		if serveErr := httpSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	})

	g.Go(func() error {
		<-gCtx.Done()
		health.SetReady(false)
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		return httpSrv.Shutdown(shutCtx)
	})

	g.Go(func() error {
		for {
			select {
			case <-gCtx.Done():
				return nil
			case msg, ok := <-framesCh:
				if !ok {
					return nil
				}
				hub.BroadcastFrame(msg.CameraID, msg.Payload)
			}
		}
	})

	g.Go(func() error {
		for {
			select {
			case <-gCtx.Done():
				return nil
			case payload, ok := <-alarmsCh:
				if !ok {
					return nil
				}
				hub.BroadcastAlarm(payload)
			}
		}
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("aegis-ws exited with error", "err", err)
		os.Exit(1)
	}
	log.Info("aegis-ws stopped cleanly")
}
