package mediamtx_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"aegisvision/backend/internal/platform/mediamtx"
)

func TestMediaMTXUpsertPathIdempotencyStatusesAndURLBuilders(t *testing.T) {
	var mu sync.Mutex
	stored := make(map[string]map[string]any)
	var mutations []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v3/config/paths/get/"):
			name := strings.TrimPrefix(r.URL.Path, "/v3/config/paths/get/")
			cfg, ok := stored[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(cfg)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v3/config/paths/add/"):
			name := strings.TrimPrefix(r.URL.Path, "/v3/config/paths/add/")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			stored[name] = body
			mutations = append(mutations, "POST "+r.URL.Path)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v3/config/paths/patch/"):
			name := strings.TrimPrefix(r.URL.Path, "/v3/config/paths/patch/")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			stored[name] = body
			mutations = append(mutations, "PATCH "+r.URL.Path)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v3/config/paths/delete/"):
			name := strings.TrimPrefix(r.URL.Path, "/v3/config/paths/delete/")
			delete(stored, name)
			mutations = append(mutations, "DELETE "+r.URL.Path)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v3/paths/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pageCount": 1,
				"items": []map[string]any{
					{"name": "cam_001", "ready": true},
					{"name": "cam_002", "ready": false},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := mediamtx.New(srv.URL, "rtsp://192.168.0.114:8554", "/whep", srv.Client())

	// 1. First upsert adds the path with TCP transport and recording params
	spec := mediamtx.PathSpec{
		StreamPath:  "cam_001",
		SourceURL:   "rtsp://192.168.0.200:554/stream1",
		RecordVideo: true,
	}
	if err := client.UpsertPath(context.Background(), spec); err != nil {
		t.Fatalf("first UpsertPath() failed: %v", err)
	}
	if len(mutations) != 1 || mutations[0] != "POST /v3/config/paths/add/cam_001" {
		t.Fatalf("mutations after add = %v, want [POST /v3/config/paths/add/cam_001]", mutations)
	}
	cfg := stored["cam_001"]
	if cfg["source"] != spec.SourceURL || cfg["rtspTransport"] != "tcp" || cfg["record"] != true {
		t.Errorf("unexpected stored path config: %+v", cfg)
	}
	if cfg["recordFormat"] != "fmp4" || cfg["recordSegmentDuration"] != "5s" || cfg["recordDeleteAfter"] != "2m" {
		t.Errorf("unexpected short-term recording params: %+v", cfg)
	}

	// 2. Second identical upsert MUST be a no-op (zero POST/PATCH calls)
	if err := client.UpsertPath(context.Background(), spec); err != nil {
		t.Fatalf("second UpsertPath() failed: %v", err)
	}
	if len(mutations) != 1 {
		t.Fatalf("identical UpsertPath should not mutate MediaMTX; mutations = %v", mutations)
	}

	// 3. Upsert with changed RecordVideo patches directly (without POST)
	spec.RecordVideo = false
	if err := client.UpsertPath(context.Background(), spec); err != nil {
		t.Fatalf("third UpsertPath() failed: %v", err)
	}
	if len(mutations) != 2 || mutations[1] != "PATCH /v3/config/paths/patch/cam_001" {
		t.Fatalf("mutations after config change = %v", mutations)
	}

	// 4. DeletePath removes existing path and skips when already absent
	if err := client.DeletePath(context.Background(), "cam_001"); err != nil {
		t.Fatalf("first DeletePath() failed: %v", err)
	}
	if err := client.DeletePath(context.Background(), "cam_001"); err != nil {
		t.Fatalf("second DeletePath() failed: %v", err)
	}
	if len(mutations) != 3 || mutations[2] != "DELETE /v3/config/paths/delete/cam_001" {
		t.Fatalf("mutations after idempotent deletes = %v", mutations)
	}

	// 5. ListPathStatuses returns ready flags per stream path
	statuses, err := client.ListPathStatuses(context.Background())
	if err != nil {
		t.Fatalf("ListPathStatuses() failed: %v", err)
	}
	if !statuses["cam_001"] || statuses["cam_002"] {
		t.Errorf("unexpected statuses = %+v", statuses)
	}

	// 6. Authorized URL builders
	if got := client.BuildAuthorizedRTSPURL("cam_001", "pull-tok-xyz"); got != "rtsp://192.168.0.114:8554/cam_001?token=pull-tok-xyz" {
		t.Errorf("BuildAuthorizedRTSPURL() = %q", got)
	}
	if got := client.BuildWHEPPath("cam_001", "play-tok-abc"); got != "/whep/cam_001/whep?token=play-tok-abc" {
		t.Errorf("BuildWHEPPath() = %q", got)
	}
	if got := client.BuildHLSPath("cam_001", "play-tok-abc"); got != "/hls/cam_001/index.m3u8?token=play-tok-abc" {
		t.Errorf("BuildHLSPath() = %q", got)
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
