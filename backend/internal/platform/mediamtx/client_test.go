package mediamtx_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/GuardHer/AegisVision/backend/internal/platform/mediamtx"
)

func TestMediaMTXUpsertPathRecordingAndURLBuilders(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.Method + " " + r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := mediamtx.New(srv.URL, "rtsp://192.168.0.114:8554", "/whep", srv.Client())

	err := client.UpsertPath(context.Background(), mediamtx.PathSpec{
		StreamPath:  "cam_001",
		SourceURL:   "rtsp://192.168.0.200:554/stream1",
		RecordVideo: true,
	})
	if err != nil {
		t.Fatalf("UpsertPath() failed: %v", err)
	}

	if receivedPath != "POST /v3/config/paths/add/cam_001" {
		t.Errorf("request = %q, want POST /v3/config/paths/add/cam_001", receivedPath)
	}
	if receivedBody["source"] != "rtsp://192.168.0.200:554/stream1" {
		t.Errorf("source = %v, want rtsp source url", receivedBody["source"])
	}
	if rec, ok := receivedBody["record"].(bool); !ok || !rec {
		t.Errorf("record = %v, want true when RecordVideo=true", receivedBody["record"])
	}
	if receivedBody["recordFormat"] != "fmp4" || receivedBody["recordSegmentDuration"] != "5s" || receivedBody["recordDeleteAfter"] != "2m" {
		t.Errorf("unexpected short-term recording params: %+v", receivedBody)
	}

	rtspURL := client.BuildAuthorizedRTSPURL("cam_001", "pull-tok-xyz")
	if rtspURL != "rtsp://192.168.0.114:8554/cam_001?token=pull-tok-xyz" {
		t.Errorf("BuildAuthorizedRTSPURL() = %q", rtspURL)
	}
	whepPath := client.BuildWHEPPath("cam_001", "play-tok-abc")
	if whepPath != "/whep/cam_001/whep?token=play-tok-abc" {
		t.Errorf("BuildWHEPPath() = %q", whepPath)
	}
}

func TestMediaMTXLiveControlAPI(t *testing.T) {
	apiURL := os.Getenv("AEGIS_TEST_MEDIAMTX_API_URL")
	if apiURL == "" {
		t.Skip("AEGIS_TEST_MEDIAMTX_API_URL not set; skipping live MediaMTX test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := mediamtx.New(apiURL, "rtsp://192.168.0.114:8554", "/whep", nil)
	pathName := "cam_test_unit"
	t.Cleanup(func() {
		_ = client.DeletePath(context.Background(), pathName)
	})

	if err := client.UpsertPath(ctx, mediamtx.PathSpec{
		StreamPath:  pathName,
		SourceURL:   "rtsp://127.0.0.1:8554/nonexistent_source",
		RecordVideo: false,
	}); err != nil {
		t.Fatalf("live UpsertPath(add) failed: %v", err)
	}
	// Upsert second time must smoothly patch existing path
	if err := client.UpsertPath(ctx, mediamtx.PathSpec{
		StreamPath:  pathName,
		SourceURL:   "rtsp://127.0.0.1:8554/nonexistent_source_v2",
		RecordVideo: true,
	}); err != nil {
		t.Fatalf("live UpsertPath(patch) failed: %v", err)
	}
	if err := client.DeletePath(ctx, pathName); err != nil {
		t.Fatalf("live DeletePath() failed: %v", err)
	}
}
