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
		httpClient:  httpClient,
	}
}

type pathConfigPayload struct {
	Source                string `json:"source"`
	SourceOnDemand        bool   `json:"sourceOnDemand"`
	Record                bool   `json:"record"`
	RecordFormat          string `json:"recordFormat,omitempty"`
	RecordSegmentDuration string `json:"recordSegmentDuration,omitempty"`
	RecordDeleteAfter     string `json:"recordDeleteAfter,omitempty"`
}

// UpsertPath adds or updates a camera path in MediaMTX via Control API v3.
func (c *Client) UpsertPath(ctx context.Context, spec PathSpec) error {
	payload := pathConfigPayload{
		Source:         spec.SourceURL,
		SourceOnDemand: false,
		Record:         spec.RecordVideo,
	}
	if spec.RecordVideo {
		payload.RecordFormat = "fmp4"
		payload.RecordSegmentDuration = "5s"
		payload.RecordDeleteAfter = "2m"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal mediamtx path config: %w", err)
	}

	escapedPath := url.PathEscape(spec.StreamPath)
	addURL := fmt.Sprintf("%s/v3/config/paths/add/%s", c.apiURL, escapedPath)
	status, respBody, err := c.doJSON(ctx, http.MethodPost, addURL, body)
	if err != nil {
		return fmt.Errorf("mediamtx add path %q: %w", spec.StreamPath, err)
	}
	if status >= 200 && status < 300 {
		return nil
	}

	// MediaMTX returns 400 when path already exists; fall back to PATCH.
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

// DeletePath removes a path from MediaMTX (treating 404 as idempotent success).
func (c *Client) DeletePath(ctx context.Context, streamPath string) error {
	delURL := fmt.Sprintf("%s/v3/config/paths/delete/%s", c.apiURL, url.PathEscape(streamPath))
	status, respBody, err := c.doJSON(ctx, http.MethodDelete, delURL, nil)
	if err != nil {
		return fmt.Errorf("mediamtx delete path %q: %w", streamPath, err)
	}
	if (status >= 200 && status < 300) || status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("mediamtx delete path %q returned %d: %s", streamPath, status, respBody)
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

func (c *Client) doJSON(ctx context.Context, method, targetURL string, body []byte) (int, string, error) {
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

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, strings.TrimSpace(string(raw)), nil
}
