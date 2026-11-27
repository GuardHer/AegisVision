package ws

import (
	"encoding/json"
	"sync"
)

// HubConfig controls per-client channel capacities for backpressure isolation.
type HubConfig struct {
	FrameBufferSize int
	AlarmBufferSize int
}

// ClientScope defines the workshop-level data access scope for a connected WebSocket client.
type ClientScope struct {
	UserID         string
	Unrestricted   bool
	AllowedZones   map[string]struct{}
	AllowedCameras map[string]struct{}
}

func (s ClientScope) allowsCamera(camID string) bool {
	if s.Unrestricted {
		return true
	}
	_, ok := s.AllowedCameras[camID]
	return ok
}

func (s ClientScope) allowsAlarm(zoneID, cameraID string) bool {
	if s.Unrestricted {
		return true
	}
	if zoneID != "" {
		if _, ok := s.AllowedZones[zoneID]; ok {
			return true
		}
	}
	if cameraID != "" {
		if _, ok := s.AllowedCameras[cameraID]; ok {
			return true
		}
	}
	return false
}

// Client represents a single authenticated browser WebSocket session.
type Client struct {
	id     string
	frames chan []byte
	alarms chan []byte
	scope  ClientScope
}

// ID returns the unique session identifier.
func (c *Client) ID() string { return c.id }

// FrameChan returns the bounded drop-oldest channel carrying transient frame_detections.
func (c *Client) FrameChan() <-chan []byte { return c.frames }

// AlarmChan returns the dedicated priority channel carrying global alarm_alert messages.
func (c *Client) AlarmChan() <-chan []byte { return c.alarms }

// Hub manages connected WebSocket clients and camera subscription routing.
type Hub struct {
	frameBufSize int
	alarmBufSize int

	mu          sync.RWMutex
	clients     map[string]*Client
	userClients map[string]map[string]*Client
	clientSubs  map[string]map[string]struct{}
	cameraIndex map[string]map[string]*Client
}

// NewHub constructs a Hub with bounded per-client buffers.
func NewHub(cfg HubConfig) *Hub {
	if cfg.FrameBufferSize <= 0 {
		cfg.FrameBufferSize = 16
	}
	if cfg.AlarmBufferSize <= 0 {
		cfg.AlarmBufferSize = 32
	}
	return &Hub{
		frameBufSize: cfg.FrameBufferSize,
		alarmBufSize: cfg.AlarmBufferSize,
		clients:      make(map[string]*Client),
		userClients:  make(map[string]map[string]*Client),
		clientSubs:   make(map[string]map[string]struct{}),
		cameraIndex:  make(map[string]map[string]*Client),
	}
}

// Register adds an unrestricted client session to the hub.
func (h *Hub) Register(clientID string) *Client {
	return h.RegisterWithScope(clientID, ClientScope{Unrestricted: true})
}

// RegisterWithScope adds a client session bound to a workshop data-access scope.
func (h *Hub) RegisterWithScope(clientID string, scope ClientScope) *Client {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := &Client{
		id:     clientID,
		frames: make(chan []byte, h.frameBufSize),
		alarms: make(chan []byte, h.alarmBufSize),
		scope:  scope,
	}
	h.clients[clientID] = c
	h.clientSubs[clientID] = make(map[string]struct{})
	if scope.UserID != "" {
		bucket, ok := h.userClients[scope.UserID]
		if !ok {
			bucket = make(map[string]*Client)
			h.userClients[scope.UserID] = bucket
		}
		bucket[clientID] = c
	}
	return c
}

// Unregister removes a client and cleans up all of its camera subscriptions.
func (h *Hub) Unregister(clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if c, exists := h.clients[clientID]; exists && c.scope.UserID != "" {
		if uBucket, ok := h.userClients[c.scope.UserID]; ok {
			delete(uBucket, clientID)
			if len(uBucket) == 0 {
				delete(h.userClients, c.scope.UserID)
			}
		}
	}

	if subs, ok := h.clientSubs[clientID]; ok {
		for camID := range subs {
			if bucket, exists := h.cameraIndex[camID]; exists {
				delete(bucket, clientID)
				if len(bucket) == 0 {
					delete(h.cameraIndex, camID)
				}
			}
		}
		delete(h.clientSubs, clientID)
	}
	delete(h.clients, clientID)
}

// SetSubscriptions replaces clientID's subscribed camera_ids atomically, enforcing the client's workshop scope.
func (h *Hub) SetSubscriptions(clientID string, cameraIDs []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	client, ok := h.clients[clientID]
	if !ok {
		return
	}

	// Remove previous subscriptions
	if oldSubs, exists := h.clientSubs[clientID]; exists {
		for camID := range oldSubs {
			if bucket, ok := h.cameraIndex[camID]; ok {
				delete(bucket, clientID)
				if len(bucket) == 0 {
					delete(h.cameraIndex, camID)
				}
			}
		}
	}

	newSubs := make(map[string]struct{}, len(cameraIDs))
	for _, camID := range cameraIDs {
		if camID == "" || !client.scope.allowsCamera(camID) {
			continue
		}
		newSubs[camID] = struct{}{}
		bucket, exists := h.cameraIndex[camID]
		if !exists {
			bucket = make(map[string]*Client)
			h.cameraIndex[camID] = bucket
		}
		bucket[clientID] = client
	}
	h.clientSubs[clientID] = newSubs
}

// UpdateUserScope atomically updates the active scope for every live connection belonging to userID
// and immediately evicts any subscribed cameras that are no longer permitted.
func (h *Hub) UpdateUserScope(userID string, scope ClientScope) {
	if userID == "" {
		return
	}
	scope.UserID = userID

	h.mu.Lock()
	defer h.mu.Unlock()

	uBucket, ok := h.userClients[userID]
	if !ok {
		return
	}
	for clientID, client := range uBucket {
		client.scope = scope
		if scope.Unrestricted {
			continue
		}
		subs := h.clientSubs[clientID]
		for camID := range subs {
			if !scope.allowsCamera(camID) {
				delete(subs, camID)
				if camBucket, exists := h.cameraIndex[camID]; exists {
					delete(camBucket, clientID)
					if len(camBucket) == 0 {
						delete(h.cameraIndex, camID)
					}
				}
			}
		}
	}
}

// BroadcastFrame delivers a transient frame_detections payload to all authorized clients subscribed to cameraID.
// Slow clients drop the oldest queued frame to keep only the freshest state without blocking other clients.
func (h *Hub) BroadcastFrame(cameraID string, payload []byte) {
	h.mu.RLock()
	bucket := h.cameraIndex[cameraID]
	targets := make([]*Client, 0, len(bucket))
	for _, c := range bucket {
		if c.scope.allowsCamera(cameraID) {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range targets {
		enqueueDropOldest(c.frames, payload)
	}
}

type alarmScopeEnvelope struct {
	ZoneID   string `json:"zone_id"`
	CameraID string `json:"camera_id"`
}

// BroadcastAlarm delivers a high-priority alarm_alert to every authorized connected client.
func (h *Hub) BroadcastAlarm(payload []byte) {
	var env alarmScopeEnvelope
	_ = json.Unmarshal(payload, &env)

	h.mu.RLock()
	targets := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		if c.scope.allowsAlarm(env.ZoneID, env.CameraID) {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range targets {
		enqueueDropOldest(c.alarms, payload)
	}
}

func enqueueDropOldest(ch chan []byte, msg []byte) {
	select {
	case ch <- msg:
		return
	default:
	}
	// Buffer full: evict oldest entry and retry non-blockingly.
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- msg:
	default:
	}
}
