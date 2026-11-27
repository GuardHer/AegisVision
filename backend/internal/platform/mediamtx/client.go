package mediamtx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxErrorBodyBytes = 4096
	maxListBodyBytes  = 4 * 1024 * 1024
)

// PathSpec describes a MediaMTX path synced from AegisVision camera configuration.
type PathSpec struct {
	StreamPath  string
	SourceURL   string
	RecordVideo bool
}

// Client interacts with MediaMTX Control API v3 and builds authorized stream URLs.
type Client struct {
	apiURL      string
	rtspBaseURL string
	whepBaseURL string
	hlsBaseURL  string
	httpClient  *http.Client
}

// New creates a MediaMTX Control API client.
func New(apiURL, rtspBaseURL, whepBaseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		apiURL:      strings.TrimRight(apiURL, "/"),
		rtspBaseURL: strings.TrimRight(rtspBaseURL, "/"),
		whepBaseURL: strings.TrimRight(whepBaseURL, "/"),
		hlsBaseURL:  "/hls",
		httpClient:  httpClient,
	}
}

// WithHLSBaseURL overrides the Nginx-proxied LL-HLS base URL prefix (defaults to "/hls").
func (c *Client) WithHLSBaseURL(hlsBaseURL string) *Client {
	if trimmed := strings.TrimRight(strings.TrimSpace(hlsBaseURL), "/"); trimmed != "" {
		c.hlsBaseURL = trimmed
	}
	return c
}

type pathConfigPayload struct {
	Source                string `json:"source"`
	SourceOnDemand        bool   `json:"sourceOnDemand"`
	RTSPTransport         string `json:"rtspTransport,omitempty"`
	Record                bool   `json:"record"`
	RecordFormat          string `json:"recordFormat,omitempty"`
	RecordSegmentDuration string `json:"recordSegmentDuration,omitempty"`
	RecordDeleteAfter     string `json:"recordDeleteAfter,omitempty"`
}

func samePathConfig(existing, desired pathConfigPayload) bool {
	if existing.Source != desired.Source ||
		existing.SourceOnDemand != desired.SourceOnDemand ||
		!strings.EqualFold(existing.RTSPTransport, desired.RTSPTransport) ||
		existing.Record != desired.Record {
		return false
	}
	if desired.Record {
		if existing.RecordFormat != desired.RecordFormat ||
			existing.RecordSegmentDuration != desired.RecordSegmentDuration ||
			existing.RecordDeleteAfter != desired.RecordDeleteAfter {
			return false
		}
	}
	return true
}

func (c *Client) getPathConfig(ctx context.Context, escapedPath string) (*pathConfigPayload, bool, error) {
	getURL := fmt.Sprintf("%s/v3/config/paths/get/%s", c.apiURL, escapedPath)
	status, rawBody, err := c.doJSONWithLimit(ctx, http.MethodGet, getURL, nil, 64*1024)
	if err != nil {
		return nil, false, err
	}
	if status == http.StatusNotFound {
		return nil, false, nil
	}
	if status < 200 || status >= 300 {
		return nil, false, fmt.Errorf("mediamtx get path config returned %d: %s", status, rawBody)
	}
	var cfg pathConfigPayload
	if err := json.Unmarshal([]byte(rawBody), &cfg); err != nil {
		return nil, false, fmt.Errorf("decode mediamtx path config: %w", err)
	}
	return &cfg, true, nil
}

// UpsertPath adds or updates a camera path in MediaMTX via Control API v3.
// When the path already exists with an identical configuration, it is a no-op
// so periodic reconciliation does not trigger spurious MediaMTX config reloads.
func (c *Client) UpsertPath(ctx context.Context, spec PathSpec) error {
	payload := pathConfigPayload{
		Source:         spec.SourceURL,
		SourceOnDemand: false,
		RTSPTransport:  "tcp",
		Record:         spec.RecordVideo,
	}
	if spec.RecordVideo {
		payload.RecordFormat = "fmp4"
		payload.RecordSegmentDuration = "5s"
		payload.RecordDeleteAfter = "2m"
	}

	escapedPath := url.PathEscape(spec.StreamPath)
	existing, exists, getErr := c.getPathConfig(ctx, escapedPath)
	if getErr == nil && exists && existing != nil {
		if samePathConfig(*existing, payload) {
			return nil
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal mediamtx path config: %w", err)
		}
		patchURL := fmt.Sprintf("%s/v3/config/paths/patch/%s", c.apiURL, escapedPath)
		patchStatus, patchBody, patchErr := c.doJSON(ctx, http.MethodPatch, patchURL, body)
		if patchErr != nil {
			return fmt.Errorf("mediamtx patch path %q: %w", spec.StreamPath, patchErr)
		}
		if patchStatus >= 200 && patchStatus < 300 {
			return nil
		}
		return fmt.Errorf("mediamtx patch path %q returned %d: %s", spec.StreamPath, patchStatus, patchBody)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal mediamtx path config: %w", err)
	}

	addURL := fmt.Sprintf("%s/v3/config/paths/add/%s", c.apiURL, escapedPath)
	status, respBody, err := c.doJSON(ctx, http.MethodPost, addURL, body)
	if err != nil {
		return fmt.Errorf("mediamtx add path %q: %w", spec.StreamPath, err)
	}
	if status >= 200 && status < 300 {
		return nil
	}

	// MediaMTX returns 400 when path already exists (e.g. concurrent add); fall back to PATCH.
	if status == http.StatusBadRequest || status == http.StatusConflict {
		patchURL := fmt.Sprintf("%s/v3/config/paths/patch/%s", c.apiURL, escapedPath)
		patchStatus, patchBody, patchErr := c.doJSON(ctx, http.MethodPatch, patchURL, body)
		if patchErr != nil {
			return fmt.Errorf("mediamtx patch path %q: %w", spec.StreamPath, patchErr)
		}
		if patchStatus >= 200 && patchStatus < 300 {
			return nil
		}
		return fmt.Errorf("mediamtx patch path %q returned %d: %s", spec.StreamPath, patchStatus, patchBody)
	}

	return fmt.Errorf("mediamtx add path %q returned %d: %s", spec.StreamPath, status, respBody)
}

// DeletePath removes a path from MediaMTX (idempotent no-op when absent).
func (c *Client) DeletePath(ctx context.Context, streamPath string) error {
	escapedPath := url.PathEscape(streamPath)
	if _, exists, getErr := c.getPathConfig(ctx, escapedPath); getErr == nil && !exists {
		return nil
	}

	delURL := fmt.Sprintf("%s/v3/config/paths/delete/%s", c.apiURL, escapedPath)
	status, respBody, err := c.doJSON(ctx, http.MethodDelete, delURL, nil)
	if err != nil {
		return fmt.Errorf("mediamtx delete path %q: %w", streamPath, err)
	}
	if (status >= 200 && status < 300) || status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("mediamtx delete path %q returned %d: %s", streamPath, status, respBody)
}

type pathListResponse struct {
	PageCount int `json:"pageCount"`
	Items     []struct {
		Name  string `json:"name"`
		Ready bool   `json:"ready"`
	} `json:"items"`
}

// ListPathStatuses queries MediaMTX /v3/paths/list across all pages and returns
// a map of stream path name -> stream readiness (true when actively receiving media).
func (c *Client) ListPathStatuses(ctx context.Context) (map[string]bool, error) {
	result := make(map[string]bool)
	for page := 0; page < 100; page++ {
		listURL := fmt.Sprintf("%s/v3/paths/list?page=%d&itemsPerPage=1000", c.apiURL, page)
		status, rawBody, err := c.doJSONWithLimit(ctx, http.MethodGet, listURL, nil, maxListBodyBytes)
		if err != nil {
			return nil, fmt.Errorf("mediamtx list paths page %d: %w", page, err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("mediamtx list paths returned %d: %s", status, rawBody)
		}
		var parsed pathListResponse
		if err := json.Unmarshal([]byte(rawBody), &parsed); err != nil {
			return nil, fmt.Errorf("decode mediamtx path list: %w", err)
		}
		for _, item := range parsed.Items {
			result[item.Name] = item.Ready
		}
		if parsed.PageCount <= 1 || page+1 >= parsed.PageCount {
			break
		}
	}
	return result, nil
}

// Ping checks whether the MediaMTX Control API v3 is reachable.
func (c *Client) Ping(ctx context.Context) error {
	status, respBody, err := c.doJSON(ctx, http.MethodGet, c.apiURL+"/v3/paths/list", nil)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	return fmt.Errorf("mediamtx ping returned %d: %s", status, respBody)
}

// BuildAuthorizedRTSPURL returns the low-latency internal RTSP URL carrying a short-lived pull token.
func (c *Client) BuildAuthorizedRTSPURL(streamPath, token string) string {
	base := fmt.Sprintf("%s/%s", c.rtspBaseURL, strings.TrimLeft(streamPath, "/"))
	if token == "" {
		return base
	}
	return fmt.Sprintf("%s?token=%s", base, url.QueryEscape(token))
}

// BuildWHEPPath returns the Nginx-proxied WebRTC WHEP endpoint carrying a short-lived play token.
func (c *Client) BuildWHEPPath(streamPath, token string) string {
	base := fmt.Sprintf("%s/%s/whep", c.whepBaseURL, strings.TrimLeft(streamPath, "/"))
	if token == "" {
		return base
	}
	return fmt.Sprintf("%s?token=%s", base, url.QueryEscape(token))
}

// BuildHLSPath returns the Nginx-proxied LL-HLS (fMP4) playlist endpoint carrying a short-lived play token.
func (c *Client) BuildHLSPath(streamPath, token string) string {
	prefix := c.hlsBaseURL
	if prefix == "" {
		prefix = "/hls"
	}
	base := fmt.Sprintf("%s/%s/index.m3u8", prefix, strings.TrimLeft(streamPath, "/"))
	if token == "" {
		return base
	}
	return fmt.Sprintf("%s?token=%s", base, url.QueryEscape(token))
}

func (c *Client) doJSON(ctx context.Context, method, targetURL string, body []byte) (int, string, error) {
	return c.doJSONWithLimit(ctx, method, targetURL, body, maxErrorBodyBytes)
}

func (c *Client) doJSONWithLimit(ctx context.Context, method, targetURL string, body []byte, limit int64) (int, string, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, strings.TrimSpace(string(raw)), nil
}
