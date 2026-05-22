package moderator

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
)

type WeightedModeratorItem struct {
	Name    string
	Weight  int
	Service TextModeratorService
}

type weightedModeratorBackend struct {
	name          string
	weight        int
	service       TextModeratorService
	currentWeight int
}

type WeightedModeratorService struct {
	mu         sync.Mutex
	backends   []*weightedModeratorBackend
	maxRuneLen int
}

var _ TextModeratorService = (*WeightedModeratorService)(nil)

func NewWeightedModeratorService(services []WeightedModeratorItem) (*WeightedModeratorService, error) {
	if len(services) == 0 {
		return nil, fmt.Errorf("services must have at least one item")
	}

	// Check weights
	allZero := true
	for _, svc := range services {
		if svc.Weight < 0 {
			return nil, fmt.Errorf("weight must be >= 0")
		}
		if svc.Weight != 0 {
			allZero = false
		}
	}

	wrrBackends := make([]*weightedModeratorBackend, len(services))
	maxLen := 0

	for i, svc := range services {
		w := svc.Weight
		if allZero {
			w = 100 // Set default weight if all weights are 0
		}

		wrrBackends[i] = &weightedModeratorBackend{
			name:          svc.Name,
			weight:        w,
			service:       svc.Service,
			currentWeight: rand.Intn(w + 1), // Random initial weight to prevent simultaneous service starts from selecting the same backend
		}

		// Calculate max length limit - use the minimum of all service limits
		svcLen := svc.Service.MaxRuneLen()
		if i == 0 || svcLen < maxLen {
			maxLen = svcLen
		}

	}

	return &WeightedModeratorService{
		backends:   wrrBackends,
		maxRuneLen: maxLen,
	}, nil
}

func (s *WeightedModeratorService) Allow(ctx context.Context, text []rune) error {
	_, service := s.getNextService()
	if service == nil {
		return fmt.Errorf("%w: no available moderator service", ErrModerationAPIFailed)
	}
	return service.Allow(ctx, text)
}

func (s *WeightedModeratorService) MaxRuneLen() int {
	return s.maxRuneLen
}

func (s *WeightedModeratorService) getNextService() (string, TextModeratorService) {
	s.mu.Lock()
	defer s.mu.Unlock()

	totalWeight := 0
	maxWeight := 0
	var selectedBackend *weightedModeratorBackend

	for _, backend := range s.backends {
		backend.currentWeight += backend.weight
		totalWeight += backend.weight
		if backend.currentWeight > maxWeight {
			maxWeight = backend.currentWeight
			selectedBackend = backend
		}
	}

	if selectedBackend == nil {
		return "", nil
	}

	selectedBackend.currentWeight -= totalWeight
	slog.Debug(fmt.Sprintf("[WeightedModeratorService] selected service: %s", selectedBackend.name))

	return selectedBackend.name, selectedBackend.service
}
