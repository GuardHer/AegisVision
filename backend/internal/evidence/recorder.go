package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/auth"
	"aegisvision/backend/internal/domain"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/storage"
)

// RecordRequest matches the RabbitMQ evidence.record JSON payload published by aegis-ingest.
type RecordRequest struct {
	AlarmID     string    `json:"alarm_id"`
	CameraID    string    `json:"camera_id"`
	TriggeredAt time.Time `json:"triggered_at"`
	PreSeconds  int       `json:"pre_seconds"`
	PostSeconds int       `json:"post_seconds"`
}

// ClipInput provides media source and duration parameters to ClipGenerator.
type ClipInput struct {
	StreamURL   string
	StreamPath  string
	TriggeredAt time.Time
	PreSeconds  int
	PostSeconds int
}

// ClipGenerator produces a faststart MP4 file at outputPath (implemented via external ffmpeg CLI).
type ClipGenerator interface {
	GenerateClip(ctx context.Context, input ClipInput, outputPath string) error
}

// VideoRepo abstracts PostgreSQL lookups and video_object_key backfill.
type VideoRepo interface {
	GetCameraStreamPath(ctx context.Context, cameraID uuid.UUID) (string, error)
	UpdateAlarmVideoEvidence(ctx context.Context, alarmID uuid.UUID, objectKey, status string) error
}

// StreamTokenMinter mints short-lived MediaMTX RTSP pull tokens in Redis for ffmpeg capture.
type StreamTokenMinter interface {
	SetStreamToken(ctx context.Context, kind, token string, payload platformredis.StreamTokenPayload, ttl time.Duration) error
}

// RecorderDeps holds explicit dependencies for asynchronous MP4 evidence recording.
type RecorderDeps struct {
	Repo        VideoRepo
	Storage     storage.ObjectStorage
	Tokens      StreamTokenMinter
	Generator   ClipGenerator
	RTSPBaseURL string
	Logger      *slog.Logger
}

// Recorder consumes evidence.record jobs, captures MP4 clips, uploads to S3, and backfills alarms.
type Recorder struct {
	repo        VideoRepo
	storage     storage.ObjectStorage
	tokens      StreamTokenMinter
	generator   ClipGenerator
	rtspBaseURL string
	log         *slog.Logger
}

// NewRecorder creates an asynchronous MP4 evidence Recorder.
func NewRecorder(deps RecorderDeps) *Recorder {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Generator == nil {
		deps.Generator = &FFmpegClipGenerator{Binary: "ffmpeg"}
	}
	return &Recorder{
		repo:        deps.Repo,
		storage:     deps.Storage,
		tokens:      deps.Tokens,
		generator:   deps.Generator,
		rtspBaseURL: strings.TrimRight(deps.RTSPBaseURL, "/"),
		log:         deps.Logger,
	}
}

// HandleMessage decodes a RabbitMQ evidence.record job and executes RecordAndUpload.
func (r *Recorder) HandleMessage(ctx context.Context, body []byte) error {
	var req RecordRequest
	if err := json.Unmarshal(body, &req); err != nil {
		r.log.Warn("drop malformed evidence.record message", "err", err)
		return nil
	}
	_, err := r.RecordAndUpload(ctx, req)
	return err
}

// RecordAndUpload generates an MP4 clip, uploads it to ObjectStorage, and backfills alarms.video_object_key.
func (r *Recorder) RecordAndUpload(ctx context.Context, req RecordRequest) (string, error) {
	alarmID, err := uuid.Parse(req.AlarmID)
	if err != nil {
		return "", fmt.Errorf("invalid alarm_id %q: %w", req.AlarmID, err)
	}
	camID, err := uuid.Parse(req.CameraID)
	if err != nil {
		return "", fmt.Errorf("invalid camera_id %q: %w", req.CameraID, err)
	}

	markFailed := func(cause error) (string, error) {
		if updateErr := r.repo.UpdateAlarmVideoEvidence(ctx, alarmID, "", domain.VideoStatusFailed); updateErr != nil {
			r.log.Warn("failed to mark alarm video_status=failed", "alarm_id", alarmID, "err", updateErr)
		}
		return "", cause
	}

	streamPath, err := r.repo.GetCameraStreamPath(ctx, camID)
	if err != nil {
		return markFailed(fmt.Errorf("resolve camera stream_path: %w", err))
	}

	tmpDir, err := os.MkdirTemp("", "aegis-evidence-*")
	if err != nil {
		return markFailed(fmt.Errorf("create temp dir for evidence clip: %w", err))
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	outFile := filepath.Join(tmpDir, alarmID.String()+".mp4")
	preSec := req.PreSeconds
	if preSec <= 0 {
		preSec = 5
	}
	postSec := req.PostSeconds
	if postSec <= 0 {
		postSec = 5
	}
	triggeredAt := req.TriggeredAt
	if triggeredAt.IsZero() {
		triggeredAt = time.Now().UTC()
	}

	cleanPath := strings.TrimLeft(streamPath, "/")
	streamURL := fmt.Sprintf("%s/%s", r.rtspBaseURL, cleanPath)
	if r.tokens != nil {
		tok, tokErr := auth.GenerateOpaqueToken()
		if tokErr != nil {
			return markFailed(fmt.Errorf("generate rtsp pull token: %w", tokErr))
		}
		tokTTL := time.Duration(preSec+postSec+60) * time.Second
		if err := r.tokens.SetStreamToken(ctx, platformredis.StreamTokenKindPull, tok, platformredis.StreamTokenPayload{
			TaskID:     alarmID.String(),
			StreamPath: cleanPath,
		}, tokTTL); err != nil {
			return markFailed(fmt.Errorf("store rtsp pull token: %w", err))
		}
		streamURL = streamURL + "?token=" + tok
	}

	clipInput := ClipInput{
		StreamURL:   streamURL,
		StreamPath:  streamPath,
		TriggeredAt: triggeredAt,
		PreSeconds:  preSec,
		PostSeconds: postSec,
	}

	clipCtx, cancel := context.WithTimeout(ctx, time.Duration(preSec+postSec+30)*time.Second)
	defer cancel()

	if err := r.generator.GenerateClip(clipCtx, clipInput, outFile); err != nil {
		return markFailed(fmt.Errorf("generate evidence clip: %w", err))
	}

	f, err := os.Open(outFile) // #nosec G304 -- outFile is created inside controlled temp dir
	if err != nil {
		return markFailed(fmt.Errorf("open generated clip: %w", err))
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return markFailed(fmt.Errorf("stat generated clip: %w", err))
	}

	objectKey := fmt.Sprintf("videos/%s/%s/%s.mp4",
		triggeredAt.UTC().Format("2006/01/02"),
		camID.String(),
		alarmID.String(),
	)

	if err := r.storage.PutObject(ctx, objectKey, f, stat.Size(), "video/mp4"); err != nil {
		return markFailed(fmt.Errorf("upload video evidence to storage: %w", err))
	}

	if err := r.repo.UpdateAlarmVideoEvidence(ctx, alarmID, objectKey, domain.VideoStatusReady); err != nil {
		return "", fmt.Errorf("backfill alarm video_object_key: %w", err)
	}
	return objectKey, nil
}

// FFmpegClipGenerator captures an MP4 clip via external ffmpeg CLI with stream copy and +faststart (zero CGO).
type FFmpegClipGenerator struct {
	Binary string
}

// GenerateClip invokes external ffmpeg CLI under ctx timeout.
func (g *FFmpegClipGenerator) GenerateClip(ctx context.Context, input ClipInput, outputPath string) error {
	bin := g.Binary
	if bin == "" {
		bin = "ffmpeg"
	}
	duration := input.PreSeconds + input.PostSeconds
	if duration <= 0 {
		duration = 10
	}
	args := []string{
		"-y",
		"-rtsp_transport", "tcp",
		"-i", input.StreamURL,
		"-t", strconv.Itoa(duration),
		"-c", "copy",
		"-movflags", "+faststart",
		outputPath,
	}
	// #nosec G204 -- ffmpeg binary and args are constructed from internal MediaMTX path and bounded ints
	cmd := exec.CommandContext(ctx, bin, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
