package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"aegisvision/backend/internal/agent"
	"aegisvision/backend/internal/platform/logger"
)

type agentFileConfig struct {
	NodeID       string                       `yaml:"node_id"`
	IPAddr       string                       `yaml:"ip_addr"`
	IngestAddr   string                       `yaml:"ingest_addr"`
	AgentToken   string                       `yaml:"agent_token"`
	MaxTasks     int                          `yaml:"max_tasks"`
	BufferDBPath string                       `yaml:"buffer_db_path"`
	LogLevel     string                       `yaml:"log_level"`
	Runtimes     map[string]agent.RuntimeSpec `yaml:"runtimes"`
}

func main() {
	cfgPath := flag.String("config", "", "Path to agent.yaml configuration file")
	flag.Parse()

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "compute-node"
	}

	cfg := agentFileConfig{
		NodeID:       hostname,
		IPAddr:       "127.0.0.1",
		IngestAddr:   "127.0.0.1:9090",
		MaxTasks:     8,
		BufferDBPath: "./data/agent-buffer.sqlite",
		LogLevel:     "info",
		Runtimes:     map[string]agent.RuntimeSpec{},
	}

	if *cfgPath != "" {
		raw, err := os.ReadFile(*cfgPath) // #nosec G304 -- config path explicitly provided by operator
		if err != nil {
			logger.New(logger.Options{Service: "aegis-agent"}).Error("read agent config failed", "err", err)
			os.Exit(1)
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			logger.New(logger.Options{Service: "aegis-agent"}).Error("parse agent config failed", "err", err)
			os.Exit(1)
		}
	}

	if v := os.Getenv("AEGIS_NODE_ID"); v != "" {
		cfg.NodeID = v
	}
	if v := os.Getenv("AEGIS_INGEST_ADDR"); v != "" {
		cfg.IngestAddr = v
	}
	if v := os.Getenv("AEGIS_AGENT_TOKEN"); v != "" {
		cfg.AgentToken = v
	}
	if v := os.Getenv("AEGIS_BUFFER_DB_PATH"); v != "" {
		cfg.BufferDBPath = v
	}

	log := logger.New(logger.Options{
		Service:    "aegis-agent",
		InstanceID: cfg.NodeID,
		Level:      cfg.LogLevel,
		Format:     "json",
	})

	buf, err := agent.NewLocalBuffer(cfg.BufferDBPath)
	if err != nil {
		log.Error("init local sqlite buffer failed", "path", cfg.BufferDBPath, "err", err)
		os.Exit(1)
	}
	defer func() { _ = buf.Close() }()

	rtMgr := agent.NewRuntimeManager(agent.RuntimeManagerDeps{
		Specs:  cfg.Runtimes,
		Logger: log,
	})
	defer rtMgr.Shutdown()

	na := agent.NewNodeAgent(agent.NodeAgentDeps{
		NodeID:            cfg.NodeID,
		Hostname:          hostname,
		IPAddr:            cfg.IPAddr,
		IngestAddr:        cfg.IngestAddr,
		AgentToken:        cfg.AgentToken,
		MaxTasks:          cfg.MaxTasks,
		HeartbeatInterval: 5 * time.Second,
		RuntimeMgr:        rtMgr,
		Buffer:            buf,
		Logger:            log,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("aegis-agent starting", "node_id", cfg.NodeID, "ingest_addr", cfg.IngestAddr)
	if err := na.Run(ctx); err != nil {
		log.Error("aegis-agent exited with error", "err", err)
		os.Exit(1)
	}
	log.Info("aegis-agent stopped cleanly")
}
