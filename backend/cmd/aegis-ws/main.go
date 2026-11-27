package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/config"
	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/logger"
	"aegisvision/backend/internal/platform/postgres"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/telemetry"
	"aegisvision/backend/internal/ws"
)

const wsScopeCacheTTL = 60 * time.Second

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

	var pgStore *postgres.Store
	if cfg.Postgres.DSN != "" {
		pgStore, err = postgres.New(bootCtx, cfg.Postgres.DSN)
		if err != nil {
			log.Error("connect postgres failed", "err", err)
			os.Exit(1)
		}
		defer pgStore.Close()
	}

	hub := ws.NewHub(ws.HubConfig{
		FrameBufferSize: 16,
		AlarmBufferSize: 64,
	})
	tokenMgr := auth.NewTokenManager(cfg.Auth.JWTSecret, cfg.Auth.AccessTokenTTL)
	health := telemetry.NewHealth("aegis-ws")
	health.AddReadinessCheck("redis", redisClient.Ping)
	if pgStore != nil {
		health.AddReadinessCheck("postgres", pgStore.Ping)
	}

	resolveScope := buildScopeResolver(pgStore, redisClient, log)

	wsSrv := ws.NewServer(ws.ServerDeps{
		Hub:           hub,
		Tokens:        tokenMgr,
		Blacklist:     redisClient,
		ScopeResolver: resolveScope,
		Health:        health,
		Logger:        log,
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

	scopeCh, unsubScope, err := redisClient.SubscribeScopeChanged(ctx)
	if err != nil {
		log.Error("subscribe redis auth:scope_changed failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = unsubScope() }()

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

	g.Go(func() error {
		for {
			select {
			case <-gCtx.Done():
				return nil
			case userID, ok := <-scopeCh:
				if !ok {
					return nil
				}
				if resolveScope == nil {
					continue
				}
				if userID == "*" {
					// Camera or zone topology changed: refresh every connected user's scope from DB.
					_ = redisClient.DeleteAllUserScopeCaches(gCtx)
					for _, activeUID := range hub.ActiveUserIDs() {
						refreshCtx, cancel := context.WithTimeout(gCtx, 5*time.Second)
						fresh, rErr := resolveScope(refreshCtx, activeUID, "")
						cancel()
						if rErr != nil {
							log.Warn("refresh ws topology scope failed; revoking active scope", "user_id", activeUID, "err", rErr)
							fresh = ws.ClientScope{UserID: activeUID, Unrestricted: false}
						}
						hub.UpdateUserScope(activeUID, fresh)
					}
					continue
				}
				refreshCtx, cancel := context.WithTimeout(gCtx, 5*time.Second)
				fresh, rErr := resolveScope(refreshCtx, userID, "")
				cancel()
				if rErr != nil {
					log.Warn("refresh ws user scope failed; revoking active scope", "user_id", userID, "err", rErr)
					fresh = ws.ClientScope{UserID: userID, Unrestricted: false}
				}
				hub.UpdateUserScope(userID, fresh)
			}
		}
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("aegis-ws exited with error", "err", err)
		os.Exit(1)
	}
	log.Info("aegis-ws stopped cleanly")
}

func buildScopeResolver(pgStore *postgres.Store, redisClient *platformredis.Client, log *slog.Logger) ws.ScopeResolver {
	if pgStore == nil {
		return nil
	}
	return func(ctx context.Context, userID, _ string) (ws.ClientScope, error) {
		if redisClient != nil {
			if cached, err := redisClient.GetUserScopeCache(ctx, userID); err == nil && cached != nil {
				return payloadToClientScope(userID, *cached), nil
			}
		}
		uid, err := uuid.Parse(userID)
		if err != nil {
			return ws.ClientScope{UserID: userID, Unrestricted: false}, err
		}
		flags, err := pgStore.Queries().GetUserZoneScopeFlags(ctx, uid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ws.ClientScope{UserID: userID, Unrestricted: false}, nil
			}
			return ws.ClientScope{UserID: userID, Unrestricted: false}, err
		}
		if !flags.Enabled {
			return ws.ClientScope{UserID: userID, Unrestricted: false}, nil
		}
		if flags.Role == domain.RoleAdmin || flags.AllZonesAccess {
			payload := platformredis.UserScopeCachePayload{Unrestricted: true}
			if redisClient != nil {
				_ = redisClient.SetUserScopeCache(ctx, userID, payload, wsScopeCacheTTL)
			}
			return ws.ClientScope{UserID: userID, Unrestricted: true}, nil
		}

		assigned, err := pgStore.Queries().ListUserZoneIDs(ctx, uid)
		if err != nil {
			return ws.ClientScope{UserID: userID, Unrestricted: false}, err
		}
		allZones, err := pgStore.Queries().ListZones(ctx)
		if err != nil {
			return ws.ClientScope{UserID: userID, Unrestricted: false}, err
		}
		children := make(map[uuid.UUID][]uuid.UUID, len(allZones))
		for _, z := range allZones {
			if z.ParentID.Valid {
				children[z.ParentID.UUID] = append(children[z.ParentID.UUID], z.ID)
			}
		}
		zoneSet := make(map[uuid.UUID]struct{}, len(assigned))
		queue := append([]uuid.UUID(nil), assigned...)
		for _, id := range queue {
			zoneSet[id] = struct{}{}
		}
		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]
			for _, ch := range children[curr] {
				if _, ok := zoneSet[ch]; !ok {
					zoneSet[ch] = struct{}{}
					queue = append(queue, ch)
				}
			}
		}

		allCams, err := pgStore.Queries().ListCameras(ctx)
		if err != nil {
			return ws.ClientScope{UserID: userID, Unrestricted: false}, err
		}
		zoneIDStrs := make([]string, 0, len(zoneSet))
		for zid := range zoneSet {
			zoneIDStrs = append(zoneIDStrs, zid.String())
		}
		camIDStrs := make([]string, 0)
		for _, cam := range allCams {
			if cam.ZoneID.Valid {
				if _, ok := zoneSet[cam.ZoneID.UUID]; ok {
					camIDStrs = append(camIDStrs, cam.ID.String())
				}
			}
		}
		payload := platformredis.UserScopeCachePayload{
			Unrestricted:     false,
			AllowedZoneIDs:   zoneIDStrs,
			AllowedCameraIDs: camIDStrs,
		}
		if redisClient != nil {
			if setErr := redisClient.SetUserScopeCache(ctx, userID, payload, wsScopeCacheTTL); setErr != nil && log != nil {
				log.Warn("cache ws user scope failed", "user_id", userID, "err", setErr)
			}
		}
		return payloadToClientScope(userID, payload), nil
	}
}

func payloadToClientScope(userID string, p platformredis.UserScopeCachePayload) ws.ClientScope {
	zones := make(map[string]struct{}, len(p.AllowedZoneIDs))
	for _, z := range p.AllowedZoneIDs {
		zones[z] = struct{}{}
	}
	cams := make(map[string]struct{}, len(p.AllowedCameraIDs))
	for _, c := range p.AllowedCameraIDs {
		cams[c] = struct{}{}
	}
	return ws.ClientScope{
		UserID:         userID,
		Unrestricted:   p.Unrestricted,
		AllowedZones:   zones,
		AllowedCameras: cams,
	}
}
