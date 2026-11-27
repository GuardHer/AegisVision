package notifier

import (
	"strings"
	"sync"
)

// Registry maps channel_type strings (e.g. "webhook") to Notifier plugin implementations.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Notifier
}

// NewRegistry creates an empty Notifier registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins: make(map[string]Notifier),
	}
}

// Register adds a Notifier plugin keyed by its Type().
func (r *Registry) Register(n Notifier) {
	if n == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins[strings.ToLower(strings.TrimSpace(n.Type()))] = n
}

// Get retrieves the Notifier plugin for channelType.
func (r *Registry) Get(channelType string) (Notifier, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.plugins[strings.ToLower(strings.TrimSpace(channelType))]
	return n, ok
}
