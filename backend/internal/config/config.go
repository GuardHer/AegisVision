package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds unified configuration shared across AegisVision platform services.
type Config struct {
	Service   ServiceConfig   `yaml:"service"`
	Log       LogConfig       `yaml:"log"`
	HTTP      HTTPConfig      `yaml:"http"`
	GRPC      GRPCConfig      `yaml:"grpc"`
	Postgres  PostgresConfig  `yaml:"postgres"`
	Redis     RedisConfig     `yaml:"redis"`
	RabbitMQ  RabbitMQConfig  `yaml:"rabbitmq"`
	Storage   StorageConfig   `yaml:"storage"`
	MediaMTX  MediaMTXConfig  `yaml:"mediamtx"`
	Auth      AuthConfig      `yaml:"auth"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
}

// ServiceConfig identifies the running process instance.
type ServiceConfig struct {
	Name       string `yaml:"name"`
	InstanceID string `yaml:"instance_id"`
	Env        string `yaml:"env"`
}

// LogConfig configures slog output behavior.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// HTTPConfig defines listen addresses for HTTP/WS/health servers.
type HTTPConfig struct {
	APIAddr        string `yaml:"api_addr"`
	WSAddr         string `yaml:"ws_addr"`
	SchedulerAddr  string `yaml:"scheduler_addr"`
	DispatcherAddr string `yaml:"dispatcher_addr"`
	PublicBaseURL  string `yaml:"public_base_url"`
}

// GRPCConfig defines listen addresses for gRPC servers.
type GRPCConfig struct {
	IngestAddr string `yaml:"ingest_addr"`
}

// PostgresConfig configures PostgreSQL connection pooling.
type PostgresConfig struct {
	DSN             string        `yaml:"dsn"`
	MaxConns        int32         `yaml:"max_conns"`
	MinConns        int32         `yaml:"min_conns"`
	MaxConnLifetime time.Duration `yaml:"max_conn_lifetime"`
}

// RedisConfig configures Redis client connections.
type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

// RabbitMQConfig configures RabbitMQ AMQP connections.
type RabbitMQConfig struct {
	URL string `yaml:"url"`
}

// StorageConfig configures evidence object storage (s3 or local).
type StorageConfig struct {
	Driver string       `yaml:"driver"`
	S3     S3Config     `yaml:"s3"`
	Local  LocalStorage `yaml:"local"`
}

// S3Config holds S3/MinIO connection parameters.
type S3Config struct {
	Endpoint     string `yaml:"endpoint"`
	Bucket       string `yaml:"bucket"`
	AccessKey    string `yaml:"access_key"`
	SecretKey    string `yaml:"secret_key"`
	Region       string `yaml:"region"`
	UsePathStyle bool   `yaml:"use_path_style"`
}

// LocalStorage holds bare-metal local directory fallback settings.
type LocalStorage struct {
	RootDir string `yaml:"root_dir"`
}

// MediaMTXConfig holds MediaMTX Control API and stream URL settings.
type MediaMTXConfig struct {
	APIURL        string `yaml:"api_url"`
	RTSPBaseURL   string `yaml:"rtsp_base_url"`
	WHEPBaseURL   string `yaml:"whep_base_url"`
	HLSBaseURL    string `yaml:"hls_base_url"`
	RecordingsDir string `yaml:"recordings_dir"`
}

// AuthConfig configures JWT, HMAC evidence signing, and Agent cluster auth.
type AuthConfig struct {
	JWTSecret          string        `yaml:"jwt_secret"`
	AccessTokenTTL     time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL    time.Duration `yaml:"refresh_token_ttl"`
	EvidenceSignSecret string        `yaml:"evidence_sign_secret"`
	EvidenceURLTTL     time.Duration `yaml:"evidence_url_ttl"`
	AgentToken         string        `yaml:"agent_token"`
}

// SchedulerConfig holds scheduler leader election and lease timings.
type SchedulerConfig struct {
	LeaseTTLSeconds      int `yaml:"lease_ttl_seconds"`
	NodeTimeoutSeconds   int `yaml:"node_timeout_seconds"`
	ReconcileIntervalSec int `yaml:"reconcile_interval_sec"`
}

// Default returns a Config populated with production-safe defaults.
func Default() *Config {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "aegis-instance"
	}
	return &Config{
		Service: ServiceConfig{
			Name:       "aegis",
			InstanceID: hostname,
			Env:        "production",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
		HTTP: HTTPConfig{
			APIAddr:        ":8081",
			WSAddr:         ":8082",
			SchedulerAddr:  ":8083",
			DispatcherAddr: ":8084",
			PublicBaseURL:  "http://127.0.0.1",
		},
		GRPC: GRPCConfig{
			IngestAddr: ":9090",
		},
		Postgres: PostgresConfig{
			MaxConns:        20,
			MinConns:        2,
			MaxConnLifetime: 30 * time.Minute,
		},
		Redis: RedisConfig{
			Addr: "127.0.0.1:6379",
		},
		Storage: StorageConfig{
			Driver: "s3",
			S3: S3Config{
				Bucket:       "aegis-evidence",
				Region:       "us-east-1",
				UsePathStyle: true,
			},
			Local: LocalStorage{
				RootDir: "./data/evidence",
			},
		},
		MediaMTX: MediaMTXConfig{
			APIURL:      "http://127.0.0.1:9997",
			RTSPBaseURL: "rtsp://127.0.0.1:8554",
			WHEPBaseURL: "/whep",
			HLSBaseURL:  "/hls",
		},
		Auth: AuthConfig{
			AccessTokenTTL:  2 * time.Hour,
			RefreshTokenTTL: 7 * 24 * time.Hour,
			EvidenceURLTTL:  24 * time.Hour,
		},
		Scheduler: SchedulerConfig{
			LeaseTTLSeconds:      30,
			NodeTimeoutSeconds:   15,
			ReconcileIntervalSec: 5,
		},
	}
}

// Load reads configuration from an optional YAML file, applies AEGIS_ env overrides,
// and performs fail-fast validation.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path) // #nosec G304 -- config path is explicitly provided by operator
		if err != nil {
			return nil, fmt.Errorf("read config file %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config yaml %q: %w", path, err)
		}
	}

	applyEnvOverrides(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	overrideString("AEGIS_SERVICE_NAME", &cfg.Service.Name)
	overrideString("AEGIS_INSTANCE_ID", &cfg.Service.InstanceID)
	overrideString("AEGIS_ENV", &cfg.Service.Env)

	overrideString("AEGIS_LOG_LEVEL", &cfg.Log.Level)
	overrideString("AEGIS_LOG_FORMAT", &cfg.Log.Format)

	overrideString("AEGIS_HTTP_API_ADDR", &cfg.HTTP.APIAddr)
	overrideString("AEGIS_HTTP_WS_ADDR", &cfg.HTTP.WSAddr)
	overrideString("AEGIS_HTTP_SCHEDULER_ADDR", &cfg.HTTP.SchedulerAddr)
	overrideString("AEGIS_HTTP_DISPATCHER_ADDR", &cfg.HTTP.DispatcherAddr)
	overrideString("AEGIS_PUBLIC_BASE_URL", &cfg.HTTP.PublicBaseURL)
	overrideString("AEGIS_GRPC_INGEST_ADDR", &cfg.GRPC.IngestAddr)

	overrideString("AEGIS_POSTGRES_DSN", &cfg.Postgres.DSN)
	overrideString("AEGIS_REDIS_ADDR", &cfg.Redis.Addr)
	overrideString("AEGIS_REDIS_PASSWORD", &cfg.Redis.Password)
	if v, ok := os.LookupEnv("AEGIS_REDIS_DB"); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Redis.DB = n
		}
	}

	overrideString("AEGIS_RABBITMQ_URL", &cfg.RabbitMQ.URL)

	overrideString("AEGIS_STORAGE_DRIVER", &cfg.Storage.Driver)
	overrideString("AEGIS_S3_ENDPOINT", &cfg.Storage.S3.Endpoint)
	overrideString("AEGIS_S3_BUCKET", &cfg.Storage.S3.Bucket)
	overrideString("AEGIS_S3_ACCESS_KEY", &cfg.Storage.S3.AccessKey)
	overrideString("AEGIS_S3_SECRET_KEY", &cfg.Storage.S3.SecretKey)
	overrideString("AEGIS_S3_REGION", &cfg.Storage.S3.Region)
	if v, ok := os.LookupEnv("AEGIS_S3_USE_PATH_STYLE"); ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Storage.S3.UsePathStyle = b
		}
	}
	overrideString("AEGIS_LOCAL_STORAGE_DIR", &cfg.Storage.Local.RootDir)

	overrideString("AEGIS_MEDIAMTX_API_URL", &cfg.MediaMTX.APIURL)
	overrideString("AEGIS_MEDIAMTX_RTSP_BASE_URL", &cfg.MediaMTX.RTSPBaseURL)
	overrideString("AEGIS_MEDIAMTX_WHEP_BASE_URL", &cfg.MediaMTX.WHEPBaseURL)
	overrideString("AEGIS_MEDIAMTX_HLS_BASE_URL", &cfg.MediaMTX.HLSBaseURL)
	overrideString("AEGIS_MEDIAMTX_RECORDINGS_DIR", &cfg.MediaMTX.RecordingsDir)

	overrideString("AEGIS_JWT_SECRET", &cfg.Auth.JWTSecret)
	overrideString("AEGIS_EVIDENCE_SIGN_SECRET", &cfg.Auth.EvidenceSignSecret)
	overrideString("AEGIS_AGENT_TOKEN", &cfg.Auth.AgentToken)
}

func overrideString(envKey string, target *string) {
	if v, ok := os.LookupEnv(envKey); ok && strings.TrimSpace(v) != "" {
		*target = v
	}
}

// Validate checks required fields and invariants at startup (fail-fast).
func (c *Config) Validate() error {
	var errs []error

	if strings.TrimSpace(c.Postgres.DSN) == "" {
		errs = append(errs, errors.New("postgres.dsn is required"))
	}
	if strings.TrimSpace(c.Redis.Addr) == "" {
		errs = append(errs, errors.New("redis.addr is required"))
	}
	if strings.TrimSpace(c.RabbitMQ.URL) == "" {
		errs = append(errs, errors.New("rabbitmq.url is required"))
	}

	switch c.Storage.Driver {
	case "s3":
		if strings.TrimSpace(c.Storage.S3.Endpoint) == "" {
			errs = append(errs, errors.New("storage.s3.endpoint is required when driver=s3"))
		}
		if strings.TrimSpace(c.Storage.S3.Bucket) == "" {
			errs = append(errs, errors.New("storage.s3.bucket is required when driver=s3"))
		}
	case "local":
		if strings.TrimSpace(c.Storage.Local.RootDir) == "" {
			errs = append(errs, errors.New("storage.local.root_dir is required when driver=local"))
		}
	default:
		errs = append(errs, fmt.Errorf("storage.driver %q must be 's3' or 'local'", c.Storage.Driver))
	}

	if len(strings.TrimSpace(c.Auth.JWTSecret)) < 16 {
		errs = append(errs, errors.New("auth.jwt_secret must be at least 16 characters"))
	}
	if len(strings.TrimSpace(c.Auth.EvidenceSignSecret)) < 16 {
		errs = append(errs, errors.New("auth.evidence_sign_secret must be at least 16 characters"))
	}

	return errors.Join(errs...)
}
