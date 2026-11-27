package evidence_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/evidence"
	platformredis "aegisvision/backend/internal/platform/redis"
	"aegisvision/backend/internal/platform/storage"
)

type spyVideoRepo struct {
	mu      sync.Mutex
	camPath string
	key     string
	status  string
}

func (r *spyVideoRepo) GetCameraStreamPath(_ context.Context, _ uuid.UUID) (string, error) {
	return r.camPath, nil
}

func (r *spyVideoRepo) UpdateAlarmVideoEvidence(_ context.Context, _ uuid.UUID, objectKey, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.key = objectKey
	r.status = status
	return nil
}

type fakeClipGenerator struct {
	data      []byte
	err       error
	lastInput evidence.ClipInput
}

func (f *fakeClipGenerator) GenerateClip(_ context.Context, input evidence.ClipInput, outputPath string) error {
	f.lastInput = input
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(outputPath, f.data, 0o600)
}

type spyTokenMinter struct {
	kind    string
	token   string
	payload platformredis.StreamTokenPayload
}

func (s *spyTokenMinter) SetStreamToken(_ context.Context, kind, token string, payload platformredis.StreamTokenPayload, _ time.Duration) error {
	s.kind = kind
	s.token = token
	s.payload = payload
	return nil
}

func TestEvidenceRecorderUpdatesAlarmVideoKey(t *testing.T) {
	localStore, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("storage.NewLocal(): %v", err)
	}

	alarmID := uuid.Must(uuid.NewV7())
	camID := uuid.Must(uuid.NewV7())
	triggeredAt := time.Date(2026, 11, 27, 8, 30, 0, 0, time.UTC)
	mp4Bytes := []byte("fake-ftyp-moov-mdat-mp4-video-evidence-payload")

	repo := &spyVideoRepo{camPath: "cam_press_1"}
	minter := &spyTokenMinter{}
	gen := &fakeClipGenerator{data: mp4Bytes}
	rec := evidence.NewRecorder(evidence.RecorderDeps{
		Repo:        repo,
		Storage:     localStore,
		Tokens:      minter,
		Generator:   gen,
		RTSPBaseURL: "rtsp://127.0.0.1:8554",
		Logger:      slog.Default(),
	})

	jobRaw, _ := json.Marshal(evidence.RecordRequest{
		AlarmID:     alarmID.String(),
		CameraID:    camID.String(),
		TriggeredAt: triggeredAt,
		PreSeconds:  5,
		PostSeconds: 5,
	})

	if err := rec.HandleMessage(context.Background(), jobRaw); err != nil {
		t.Fatalf("HandleMessage(success) failed: %v", err)
	}
	if minter.kind != platformredis.StreamTokenKindPull || minter.token == "" || minter.payload.StreamPath != "cam_press_1" {
		t.Fatalf("expected minted stream:pull token for cam_press_1, got %+v", minter)
	}
	wantURL := "rtsp://127.0.0.1:8554/cam_press_1?token=" + minter.token
	if gen.lastInput.StreamURL != wantURL {
		t.Fatalf("expected authorized StreamURL %q, got %q", wantURL, gen.lastInput.StreamURL)
	}

	wantPrefix := "videos/2026/11/27/" + camID.String() + "/" + alarmID.String() + ".mp4"
	if repo.status != domain.VideoStatusReady || repo.key != wantPrefix {
		t.Fatalf("repo updated to (status=%q, key=%q), want (%q, %q)", repo.status, repo.key, domain.VideoStatusReady, wantPrefix)
	}

	// Verify MP4 bytes actually uploaded to ObjectStorage
	rc, _, err := localStore.GetObject(context.Background(), repo.key, "")
	if err != nil {
		t.Fatalf("GetObject(%q) failed: %v", repo.key, err)
	}
	gotBytes, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(gotBytes) != string(mp4Bytes) {
		t.Fatalf("uploaded mp4 content mismatch: got %q", gotBytes)
	}

	// Failure path must mark video_status = failed without panicking
	failRepo := &spyVideoRepo{camPath: "cam_press_1"}
	failRec := evidence.NewRecorder(evidence.RecorderDeps{
		Repo:        failRepo,
		Storage:     localStore,
		Generator:   &fakeClipGenerator{err: errors.New("ffmpeg exit 1")},
		RTSPBaseURL: "rtsp://127.0.0.1:8554",
		Logger:      slog.Default(),
	})
	err = failRec.HandleMessage(context.Background(), jobRaw)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("expected generator error, got %v", err)
	}
	if failRepo.status != domain.VideoStatusFailed || failRepo.key != "" {
		t.Fatalf("expected failed video_status with empty key, got (%q, %q)", failRepo.status, failRepo.key)
	}
}
