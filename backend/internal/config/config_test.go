package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"aegisvision/backend/internal/config"
)

func TestConfigLoadEnvOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yamlContent := `
service:
  name: aegis-api
  instance_id: api-1
  env: development
log:
  level: info
  format: json
postgres:
  dsn: postgres://yaml_user:yaml_pass@127.0.0.1:5432/yaml_db?sslmode=disable
redis:
  addr: 127.0.0.1:6379
  password: yaml_redis_password
rabbitmq:
  url: amqp://yaml_user:yaml_pass@127.0.0.1:5672/
storage:
  driver: s3
  s3:
    endpoint: http://127.0.0.1:9000
    bucket: aegis-evidence
    access_key: yaml_ak
    secret_key: yaml_sk
    region: us-east-1
    use_path_style: true
auth:
  jwt_secret: yaml-jwt-secret-must-be-long-enough
  evidence_sign_secret: yaml-sign-secret-must-be-long-enough
  agent_token: yaml-agent-token
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	t.Setenv("AEGIS_POSTGRES_DSN", "postgres://aegis:secret@192.168.0.114:15432/aegisvision?sslmode=disable")
	t.Setenv("AEGIS_REDIS_ADDR", "192.168.0.114:6379")
	t.Setenv("AEGIS_REDIS_PASSWORD", "env_redis_pass")
	t.Setenv("AEGIS_RABBITMQ_URL", "amqp://aegis:mqpass@192.168.0.114:5672/")
	t.Setenv("AEGIS_S3_ENDPOINT", "http://192.168.0.114:9000")
	t.Setenv("AEGIS_JWT_SECRET", "env-jwt-secret-1234567890")
	t.Setenv("AEGIS_EVIDENCE_SIGN_SECRET", "env-evidence-sign-secret-1234567890")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load() unexpected error: %v", err)
	}

	if cfg.Postgres.DSN != "postgres://aegis:secret@192.168.0.114:15432/aegisvision?sslmode=disable" {
		t.Errorf("Postgres.DSN = %q, want env override", cfg.Postgres.DSN)
	}
	if cfg.Redis.Addr != "192.168.0.114:6379" {
		t.Errorf("Redis.Addr = %q, want 192.168.0.114:6379", cfg.Redis.Addr)
	}
	if cfg.Redis.Password != "env_redis_pass" {
		t.Errorf("Redis.Password = %q, want env_redis_pass", cfg.Redis.Password)
	}
	if cfg.RabbitMQ.URL != "amqp://aegis:mqpass@192.168.0.114:5672/" {
		t.Errorf("RabbitMQ.URL = %q, want env override", cfg.RabbitMQ.URL)
	}
	if cfg.Storage.S3.Endpoint != "http://192.168.0.114:9000" {
		t.Errorf("Storage.S3.Endpoint = %q, want http://192.168.0.114:9000", cfg.Storage.S3.Endpoint)
	}
	if cfg.Auth.JWTSecret != "env-jwt-secret-1234567890" {
		t.Errorf("Auth.JWTSecret = %q, want env override", cfg.Auth.JWTSecret)
	}
	if cfg.Auth.EvidenceSignSecret != "env-evidence-sign-secret-1234567890" {
		t.Errorf("Auth.EvidenceSignSecret = %q, want env override", cfg.Auth.EvidenceSignSecret)
	}
	if cfg.HTTP.APIAddr == "" || cfg.HTTP.WSAddr == "" || cfg.GRPC.IngestAddr == "" {
		t.Errorf("expected default listen addresses to be populated, got http=%+v grpc=%+v", cfg.HTTP, cfg.GRPC)
	}
}

func TestConfigValidateFailFast(t *testing.T) {
	cfg := &config.Config{}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected empty Config.Validate() to return error, got nil")
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "invalid.yaml")
	yamlContent := `
postgres:
  dsn: postgres://aegis:pass@127.0.0.1:5432/aegisvision?sslmode=disable
redis:
  addr: 127.0.0.1:6379
rabbitmq:
  url: amqp://aegis:pass@127.0.0.1:5672/
storage:
  driver: unsupported_driver
auth:
  jwt_secret: super-secret-jwt-key
  evidence_sign_secret: super-secret-sign-key
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	if _, err := config.Load(cfgPath); err == nil {
		t.Fatal("expected config.Load() with unsupported storage driver to fail, got nil")
	}
}
