package softcircuitbreaker

import (
	"log/slog"
	"sync"
)

// BreakerKey identifies one process-local backend breaker.
type BreakerKey struct {
	ModelName   string
	BackendName string
}

// Registry is a process-local map of breaker entries that outlives any one load balancer.
// A nil Registry is disabled: GetOrCreate returns nil.
// policy is the default used when GetOrCreate is called with a nil policy.
type Registry struct {
	mu      sync.RWMutex
	entries map[BreakerKey]*Entry
	policy  Policy
}

// NewRegistry freezes the default policy used when GetOrCreate receives a nil policy.
func NewRegistry(policy Policy) (*Registry, error) {
	normalized, err := normalizePolicy(policy)
	if err != nil {
		return nil, err
	}
	return &Registry{
		entries: make(map[BreakerKey]*Entry),
		policy:  clonePolicy(normalized),
	}, nil
}

// GetOrCreate returns the entry for key.
//
//   - policy == nil: create with the Registry default policy; if the key exists, return the cache.
//   - policy != nil and Same as the cached entry: return the cache.
//   - policy != nil and not Same: replace the entry (windows restart in NORMAL).
//   - policy is invalid and the key already exists: log, keep the cached entry, and return it.
func (r *Registry) GetOrCreate(key BreakerKey, policy *Policy) (*Entry, error) {
	if r == nil {
		return nil, nil
	}

	r.mu.RLock()
	entry, ok := r.entries[key]
	r.mu.RUnlock()
	if ok && (policy == nil || entry.policy.Same(*policy)) {
		return entry, nil
	}

	resolved, err := r.resolvePolicy(policy)

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check after the write lock: another goroutine may have created or
	// replaced this key between RUnlock and Lock. Reuse it when policy is nil
	// (registry default) or Same as the cached entry.
	if entry, ok = r.entries[key]; ok && (policy == nil || entry.policy.Same(*policy)) {
		return entry, nil
	}
	if err != nil {
		if !ok {
			return nil, err
		}
		slog.Warn("softcircuitbreaker: invalid policy, keeping existing entry",
			"model_name", key.ModelName,
			"backend_name", key.BackendName,
			"err", err,
		)
		return entry, nil
	}
	entry = &Entry{
		key:    key,
		policy: clonePolicy(resolved),
		mode:   ModeNormal,
	}
	r.entries[key] = entry
	return entry, nil
}

func (r *Registry) resolvePolicy(policy *Policy) (Policy, error) {
	if policy == nil {
		return r.policy, nil
	}
	return normalizePolicy(*policy)
}
