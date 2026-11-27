package ws

import (
	"sync"
)

// HubConfig controls per-client channel capacities for backpressure isolation.
type HubConfig struct {
	FrameBufferSize int
	AlarmBufferSize int
}

// Client represents a single authenticated browser WebSocket session.
type Client struct {
	id     string
	frames chan []byte
	alarms chan []byte
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
		clientSubs:   make(map[string]map[string]struct{}),
		cameraIndex:  make(map[string]map[string]*Client),
	}
}

// Register adds a new client session to the hub.
func (h *Hub) Register(clientID string) *Client {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := &Client{
		id:     clientID,
		frames: make(chan []byte, h.frameBufSize),
		alarms: make(chan []byte, h.alarmBufSize),
	}
	h.clients[clientID] = c
	h.clientSubs[clientID] = make(map[string]struct{})
	return c
}

// Unregister removes a client and cleans up all of its camera subscriptions.
func (h *Hub) Unregister(clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

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

// SetSubscriptions replaces clientID's subscribed camera_ids atomically.
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
		if camID == "" {
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

// BroadcastFrame delivers a transient frame_detections payload to all clients subscribed to cameraID.
// Slow clients drop the oldest queued frame to keep only the freshest state without blocking other clients.
func (h *Hub) BroadcastFrame(cameraID string, payload []byte) {
	h.mu.RLock()
	bucket := h.cameraIndex[cameraID]
	targets := make([]*Client, 0, len(bucket))
	for _, c := range bucket {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		enqueueDropOldest(c.frames, payload)
	}
}

// BroadcastAlarm delivers a high-priority alarm_alert to every connected client.
func (h *Hub) BroadcastAlarm(payload []byte) {
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		targets = append(targets, c)
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
