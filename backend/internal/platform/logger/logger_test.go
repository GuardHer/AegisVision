package logger_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GuardHer/AegisVision/backend/internal/platform/logger"
)

func TestLoggerJSONAndRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Options{
		Service:    "aegis-api",
		InstanceID: "api-test-1",
		Level:      "info",
		Format:     "json",
		Writer:     &buf,
	})

	l.Info("booting", "dsn", logger.RedactURL("postgres://aegis:supersecret@192.168.0.114:15432/aegisvision?sslmode=disable"))

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("expected valid JSON log line, got %q: %v", buf.String(), err)
	}
	if record["service"] != "aegis-api" || record["instance_id"] != "api-test-1" {
		t.Errorf("unexpected service/instance_id fields: %+v", record)
	}
	if record["dsn"] != "postgres://aegis:%2A%2A%2A@192.168.0.114:15432/aegisvision?sslmode=disable" {
		t.Errorf("expected redacted password in dsn, got %v", record["dsn"])
	}
	if logger.RedactSecret("my-secret") != "***" || logger.RedactSecret("") != "" {
		t.Errorf("unexpected RedactSecret behavior")
	}
}
