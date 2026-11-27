package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
// Mode is "" / "video" for MP4 clip recording, or "snapshot" for single-frame JPEG fallback.
type RecordRequest struct {
	Mode        string    `json:"mode,omitempty"`
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

// SnapshotGenerator grabs a single JPEG frame from an authorized RTSP URL (implemented via external ffmpeg CLI).
type SnapshotGenerator interface {
	GenerateSnapshot(ctx context.Context, streamURL, outputPath string) error
}

// VideoRepo abstracts PostgreSQL lookups and video/image evidence backfill.
type VideoRepo interface {
	GetCameraStreamPath(ctx context.Context, cameraID uuid.UUID) (string, error)
	UpdateAlarmVideoEvidence(ctx context.Context, alarmID uuid.UUID, objectKey, status string) error
	UpdateAlarmImageEvidence(ctx context.Context, alarmID uuid.UUID, objectKey string) error
}

// StreamTokenMinter mints short-lived MediaMTX RTSP pull tokens in Redis for ffmpeg capture.
type StreamTokenMinter interface {
	SetStreamToken(ctx context.Context, kind, token string, payload platformredis.StreamTokenPayload, ttl time.Duration) error
}

// RecorderDeps holds explicit dependencies for asynchronous MP4 & snapshot evidence capture.
type RecorderDeps struct {
	Repo          VideoRepo
	Storage       storage.ObjectStorage
	Tokens        StreamTokenMinter
	Generator     ClipGenerator
	Snapshotter   SnapshotGenerator
	RTSPBaseURL   string
	RecordingsDir string
	Logger        *slog.Logger
}

// Recorder consumes evidence.record jobs, captures MP4 clips or fallback snapshots, uploads to S3, and backfills alarms.
type Recorder struct {
	repo        VideoRepo
	storage     storage.ObjectStorage
	tokens      StreamTokenMinter
	generator   ClipGenerator
	snapshotter SnapshotGenerator
	rtspBaseURL string
	log         *slog.Logger
}

// NewRecorder creates an asynchronous MP4 & snapshot evidence Recorder.
func NewRecorder(deps RecorderDeps) *Recorder {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Generator == nil {
		deps.Generator = &FFmpegClipGenerator{
			Binary:        "ffmpeg",
			RecordingsDir: deps.RecordingsDir,
		}
	}
	snap := deps.Snapshotter
	if snap == nil {
		if sg, ok := deps.Generator.(SnapshotGenerator); ok {
			snap = sg
		} else {
			snap = &FFmpegClipGenerator{Binary: "ffmpeg", RecordingsDir: deps.RecordingsDir}
		}
	}
	return &Recorder{
		repo:        deps.Repo,
		storage:     deps.Storage,
		tokens:      deps.Tokens,
		generator:   deps.Generator,
		snapshotter: snap,
		rtspBaseURL: strings.TrimRight(deps.RTSPBaseURL, "/"),
		log:         deps.Logger,
	}
}

// HandleMessage decodes a RabbitMQ evidence.record job and dispatches video clip or snapshot fallback capture.
func (r *Recorder) HandleMessage(ctx context.Context, body []byte) error {
	var req RecordRequest
	if err := json.Unmarshal(body, &req); err != nil {
		r.log.Warn("drop malformed evidence.record message", "err", err)
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(req.Mode), "snapshot") {
		_, err := r.CaptureSnapshotAndUpload(ctx, req)
		return err
	}
	_, err := r.RecordAndUpload(ctx, req)
	return err
}

// CaptureSnapshotAndUpload grabs a single JPEG frame from MediaMTX, uploads it to storage,
// and backfills alarms.image_object_key without mutating video_status.
func (r *Recorder) CaptureSnapshotAndUpload(ctx context.Context, req RecordRequest) (string, error) {
	alarmID, err := uuid.Parse(req.AlarmID)
	if err != nil {
		return "", fmt.Errorf("invalid alarm_id %q: %w", req.AlarmID, err)
	}
	camID, err := uuid.Parse(req.CameraID)
	if err != nil {
		return "", fmt.Errorf("invalid camera_id %q: %w", req.CameraID, err)
	}
	streamPath, err := r.repo.GetCameraStreamPath(ctx, camID)
	if err != nil {
		return "", fmt.Errorf("resolve camera stream_path: %w", err)
	}
	triggeredAt := req.TriggeredAt
	if triggeredAt.IsZero() {
		triggeredAt = time.Now().UTC()
	}

	tmpDir, err := os.MkdirTemp("", "aegis-snapshot-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir for snapshot fallback: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	outFile := filepath.Join(tmpDir, alarmID.String()+".jpg")
	cleanPath := strings.TrimLeft(streamPath, "/")
	streamURL := fmt.Sprintf("%s/%s", r.rtspBaseURL, cleanPath)
	if r.tokens != nil {
		tok, tokErr := auth.GenerateOpaqueToken()
		if tokErr != nil {
			return "", fmt.Errorf("generate rtsp pull token: %w", tokErr)
		}
		if err := r.tokens.SetStreamToken(ctx, platformredis.StreamTokenKindPull, tok, platformredis.StreamTokenPayload{
			TaskID:     alarmID.String(),
			StreamPath: cleanPath,
		}, 60*time.Second); err != nil {
			return "", fmt.Errorf("store rtsp pull token: %w", err)
		}
		streamURL = streamURL + "?token=" + tok
	}

	snapCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := r.snapshotter.GenerateSnapshot(snapCtx, streamURL, outFile); err != nil {
		return "", fmt.Errorf("generate snapshot fallback: %w", err)
	}

	f, err := os.Open(outFile) // #nosec G304 -- outFile is inside controlled temp dir
	if err != nil {
		return "", fmt.Errorf("open generated snapshot: %w", err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat generated snapshot: %w", err)
	}

	objectKey := fmt.Sprintf("images/%s/%s/%s.jpg",
		triggeredAt.UTC().Format("2006/01/02"),
		camID.String(),
		alarmID.String(),
	)
	if err := r.storage.PutObject(ctx, objectKey, f, stat.Size(), "image/jpeg"); err != nil {
		return "", fmt.Errorf("upload snapshot fallback to storage: %w", err)
	}
	if err := r.repo.UpdateAlarmImageEvidence(ctx, alarmID, objectKey); err != nil {
		return "", fmt.Errorf("backfill alarm image_object_key: %w", err)
	}
	return objectKey, nil
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
// When RecordingsDir is configured and matching MediaMTX fMP4 segments exist, it stitches pre+post alarm segments
// losslessly via ffmpeg concat demuxer; otherwise it falls back to authorized live RTSP capture.
type FFmpegClipGenerator struct {
	Binary        string
	RecordingsDir string
}

// FindRecordingSegments returns chronologically ordered MediaMTX recording segments overlapping
// [triggeredAt - preSec - 10s, triggeredAt + postSec + 10s] under recordingsDir/<streamPath>.
func FindRecordingSegments(recordingsDir, streamPath string, triggeredAt time.Time, preSec, postSec int) []string {
	if strings.TrimSpace(recordingsDir) == "" || strings.TrimSpace(streamPath) == "" {
		return nil
	}
	cleanPath := filepath.Clean(strings.TrimLeft(streamPath, "/\\"))
	if cleanPath == "." || strings.Contains(cleanPath, "..") {
		return nil
	}
	dir := filepath.Join(recordingsDir, cleanPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	windowStart := triggeredAt.Add(-time.Duration(preSec+10) * time.Second)
	windowEnd := triggeredAt.Add(time.Duration(postSec+10) * time.Second)

	type segEntry struct {
		path    string
		modTime time.Time
	}
	var matched []segEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lower := strings.ToLower(entry.Name())
		if !strings.HasSuffix(lower, ".mp4") && !strings.HasSuffix(lower, ".fmp4") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() == 0 {
			continue
		}
		mt := info.ModTime()
		if !mt.Before(windowStart) && !mt.After(windowEnd) {
			matched = append(matched, segEntry{
				path:    filepath.Join(dir, entry.Name()),
				modTime: mt,
			})
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].modTime.Equal(matched[j].modTime) {
			return matched[i].path < matched[j].path
		}
		return matched[i].modTime.Before(matched[j].modTime)
	})
	out := make([]string, 0, len(matched))
	for _, m := range matched {
		out = append(out, m.path)
	}
	return out
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

	// Prefer lossless MediaMTX fMP4 ring-buffer stitching when recordings are available.
	if segments := FindRecordingSegments(g.RecordingsDir, input.StreamPath, input.TriggeredAt, input.PreSeconds, input.PostSeconds); len(segments) > 0 {
		listPath := outputPath + ".concat.txt"
		var b strings.Builder
		for _, seg := range segments {
			escaped := strings.ReplaceAll(seg, "'", "'\\''")
			b.WriteString("file '")
			b.WriteString(escaped)
			b.WriteString("'\n")
		}
		if writeErr := os.WriteFile(listPath, []byte(b.String()), 0o600); writeErr == nil {
			defer func() { _ = os.Remove(listPath) }()
			concatArgs := []string{
				"-y",
				"-f", "concat",
				"-safe", "0",
				"-i", listPath,
				"-t", strconv.Itoa(duration),
				"-c", "copy",
				"-movflags", "+faststart",
				outputPath,
			}
			// #nosec G204 -- ffmpeg binary and args are constructed from controlled recording dir and bounded ints
			cmd := exec.CommandContext(ctx, bin, concatArgs...)
			if _, concatErr := cmd.CombinedOutput(); concatErr == nil {
				return nil
			}
		}
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

// GenerateSnapshot grabs a single JPEG frame from an authorized RTSP stream via external ffmpeg CLI.
func (g *FFmpegClipGenerator) GenerateSnapshot(ctx context.Context, streamURL, outputPath string) error {
	bin := g.Binary
	if bin == "" {
		bin = "ffmpeg"
	}
	args := []string{
		"-y",
		"-rtsp_transport", "tcp",
		"-i", streamURL,
		"-frames:v", "1",
		"-q:v", "3",
		outputPath,
	}
	// #nosec G204 -- ffmpeg binary and args are constructed from controlled internal MediaMTX path
	cmd := exec.CommandContext(ctx, bin, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg snapshot failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
