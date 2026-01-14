package ratelimiter

import (
	"context"
	"testing"
)

func setContextKey(ctx context.Context, key contextKey) context.Context {
	return context.WithValue(ctx, key, "test")
}

func TestContextKey(t *testing.T) {
	ctx := context.Background()
	ctx = setContextKey(ctx, priorityColorKey)
	if ctx.Value(priorityColorKey) != "test" {
		t.Errorf("expected test, got %s", ctx.Value(priorityColorKey))
	}
}

func TestFilterIncreasingRates(t *testing.T) {
	tests := []struct {
		name             string
		rates            []int
		expectedRates    []int
		expectedFiltered bool
	}{
		{
			name:             "strictly increasing",
			rates:            []int{100, 200, 300},
			expectedRates:    []int{100, 200, 300},
			expectedFiltered: false,
		},
		{
			name:             "not increasing - equal values",
			rates:            []int{100, 200, 200, 300},
			expectedRates:    []int{100, 200},
			expectedFiltered: true,
		},
		{
			name:             "not increasing - decreasing",
			rates:            []int{100, 200, 150, 300},
			expectedRates:    []int{100, 200},
			expectedFiltered: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filteredRates, filtered := filterIncreasingRates(tt.rates)

			if filtered != tt.expectedFiltered {
				t.Errorf("expected filtered=%v, got %v", tt.expectedFiltered, filtered)
			}

			if len(filteredRates) != len(tt.expectedRates) {
				t.Errorf("expected length %d, got %d", len(tt.expectedRates), len(filteredRates))
			}

			for i := range filteredRates {
				if filteredRates[i] != tt.expectedRates[i] {
					t.Errorf("expected rates[%d]=%d, got %d", i, tt.expectedRates[i], filteredRates[i])
				}
			}
		})
	}
}

func TestCalculatePriority(t *testing.T) {
	rates := []int{100, 200, 300}

	tests := []struct {
		name     string
		value    int
		expected int
	}{
		{
			name:     "value <= 300 (last) -> priority 0",
			value:    300,
			expected: 0,
		},
		{
			name:     "value <= 200 (second) -> priority 1",
			value:    200,
			expected: 1,
		},
		{
			name:     "value <= 100 (first) -> priority 2",
			value:    100,
			expected: 2,
		},
		{
			name:     "value > all rates -> priority 0",
			value:    400,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			priority := CalculatePriority(tt.value, rates)
			if priority != tt.expected {
				t.Errorf("expected priority %d, got %d", tt.expected, priority)
			}
		})
	}
}

func TestFilterDecreasingRates(t *testing.T) {
	tests := []struct {
		name             string
		rates            []int
		expectedRates    []int
		expectedFiltered bool
	}{
		{
			name:             "strictly decreasing",
			rates:            []int{300, 200, 100, 50},
			expectedRates:    []int{300, 200, 100, 50},
			expectedFiltered: false,
		},
		{
			name:             "not decreasing - equal values",
			rates:            []int{300, 200, 200, 100},
			expectedRates:    []int{300, 200},
			expectedFiltered: true,
		},
		{
			name:             "not decreasing - increasing",
			rates:            []int{300, 200, 250, 100},
			expectedRates:    []int{300, 200},
			expectedFiltered: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filteredRates, filtered := filterDecreasingRates(tt.rates)

			if filtered != tt.expectedFiltered {
				t.Errorf("expected filtered=%v, got %v", tt.expectedFiltered, filtered)
			}

			if len(filteredRates) != len(tt.expectedRates) {
				t.Errorf("expected length %d, got %d", len(tt.expectedRates), len(filteredRates))
			}

			for i := range filteredRates {
				if filteredRates[i] != tt.expectedRates[i] {
					t.Errorf("expected rates[%d]=%d, got %d", i, tt.expectedRates[i], filteredRates[i])
				}
			}
		})
	}
}

func TestGetPriorityFromContextAndSetPriorityToContext(t *testing.T) {
	tests := []struct {
		name         string
		setupContext func() context.Context
		expected     int
		ok           bool
	}{
		{
			name: "priority set correctly",
			setupContext: func() context.Context {
				ctx := context.Background()
				ctx = SetPriorityToContext(ctx, 2)
				return ctx
			},
			expected: 2,
			ok:       true,
		},
		{
			name: "no priority in context",
			setupContext: func() context.Context {
				return context.Background()
			},
			expected: 0,
			ok:       false,
		},
		{
			name: "malformed priority string",
			setupContext: func() context.Context {
				ctx := context.Background()
				ctx = context.WithValue(ctx, priorityColorKey, "invalid")
				return ctx
			},
			expected: 0,
			ok:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupContext()
			p, ok := GetPriorityFromContext(ctx)
			if ok != tt.ok {
				t.Fatalf("expected ok=%v, got %v", tt.ok, ok)
			}
			if p != tt.expected {
				t.Fatalf("expected priority=%d, got %d", tt.expected, p)
			}
		})
	}
}
