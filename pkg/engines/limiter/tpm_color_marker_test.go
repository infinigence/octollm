package limiter

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/octollm"
)

// closeResponse consumes and closes resp, which triggers the TPM record.
func closeResponse(t *testing.T, resp *octollm.Response) {
	t.Helper()
	if resp.Stream != nil {
		for range resp.Stream.Chan() {
		}
		resp.Stream.Close()
		return
	}
	if resp.Body != nil {
		require.NoError(t, resp.Body.Close())
	}
}

// capturePriorityEngine records the priority it sees under ns, then delegates to next.
type capturePriorityEngine struct {
	ns       string
	next     octollm.Engine
	priority int
	ok       bool
	calls    int
}

func (c *capturePriorityEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	c.calls++
	c.priority, c.ok = priorityFromNamespace(req.Context(), c.ns)
	return c.next.Process(req)
}

func TestTPMMarkerPriority(t *testing.T) {
	tests := []struct {
		name     string
		rates    []int
		tpm      int64
		want     int
		wantDeny bool
	}{
		{"idle tenant gets highest", []int{1000, 600, 300}, 0, 2, false},
		{"just below first band", []int{1000, 600, 300}, 999, 2, false},
		{"second band", []int{1000, 600, 300}, 1000, 1, false},
		{"just below second band", []int{1000, 600, 300}, 1599, 1, false},
		{"third band", []int{1000, 600, 300}, 1600, 0, false},
		{"just below total", []int{1000, 600, 300}, 1899, 0, false},
		{"total used up", []int{1000, 600, 300}, 1900, 0, true},
		{"zero lower bands", []int{3, 0, 0}, 2, 2, false},
		{"zero lower bands used up", []int{3, 0, 0}, 3, 0, true},
		{"zero top band skipped", []int{0, 5}, 0, 0, false},
		{"zero only band rejects", []int{0}, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tpmMarkerPriority(tt.rates, tt.tpm)
			assert.Equal(t, !tt.wantDeny, ok)
			if !tt.wantDeny {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestNewTPMColorMarkerEngine_Validation(t *testing.T) {
	_, client := newTPMTestRedis(t)
	next := &nextStubEngine{}

	_, err := NewTPMColorMarkerEngine(client, "k", []int{1}, "ns", nil)
	assert.Error(t, err)

	_, err = NewTPMColorMarkerEngine(nil, "k", []int{1}, "ns", next)
	assert.Error(t, err)

	e, err := NewTPMColorMarkerEngine(nil, "k", nil, "ns", next)
	require.NoError(t, err)
	assert.Nil(t, e.rates)

	e, err = NewTPMColorMarkerEngine(client, "k", []int{5, -1, 2}, "ns", next)
	require.NoError(t, err)
	assert.Equal(t, []int{5, 0, 2}, e.rates)
}

func TestTPMColorMarker_PassThroughWithoutRates(t *testing.T) {
	capture := &capturePriorityEngine{ns: "ns", next: usageEngine(10, false)}
	e, err := NewTPMColorMarkerEngine(nil, "k", nil, "ns", capture)
	require.NoError(t, err)

	req := newLimiterTestRequest(t)
	resp, err := e.Process(req)
	require.NoError(t, err)
	closeResponse(t, resp)

	assert.Equal(t, 1, capture.calls)
	assert.False(t, capture.ok)
	p, _ := GetMarkerPriority(req)
	a, _ := GetMarkerAction(req)
	assert.Equal(t, 0, p)
	assert.Equal(t, ColorActionAllow, a)
}

func TestTPMColorMarker_MarksPriorityAndRecordsUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			_, client := newTPMTestRedis(t)
			mustRecordTPM(t, client, "tenant", 1200)

			capture := &capturePriorityEngine{ns: "ns", next: usageEngine(42, stream)}
			e, err := NewTPMColorMarkerEngine(client, "tenant", []int{1000, 600, 300}, "ns", capture)
			require.NoError(t, err)

			req := newLimiterTestRequest(t)
			resp, err := e.Process(req)
			require.NoError(t, err)

			assert.True(t, capture.ok)
			assert.Equal(t, 1, capture.priority)
			p, _ := GetMarkerPriority(req)
			a, _ := GetMarkerAction(req)
			assert.Equal(t, 1, p)
			assert.Equal(t, ColorActionAllow, a)

			// Usage is recorded only once the response is closed.
			assert.Equal(t, int64(1200), mustReadTPM(t, client, "tenant"))
			closeResponse(t, resp)
			assert.Equal(t, int64(1242), mustReadTPM(t, client, "tenant"))
		})
	}
}

func TestTPMColorMarker_RejectsWhenEveryBandUsedUp(t *testing.T) {
	_, client := newTPMTestRedis(t)
	mustRecordTPM(t, client, "tenant", 1900)

	next := &nextStubEngine{}
	e, err := NewTPMColorMarkerEngine(client, "tenant", []int{1000, 600, 300}, "ns", next)
	require.NoError(t, err)

	req := newLimiterTestRequest(t)
	resp, err := e.Process(req)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, ErrTPMRateLimitReached)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 0, next.callCount)

	a, _ := GetMarkerAction(req)
	assert.Equal(t, ColorActionDeny, a)
}

func TestTPMColorMarker_NoRecordOnNextError(t *testing.T) {
	mr, client := newTPMTestRedis(t)
	boom := errors.New("boom")
	e, err := NewTPMColorMarkerEngine(client, "tenant", []int{100}, "ns", &nextStubEngine{err: boom})
	require.NoError(t, err)

	_, err = e.Process(newLimiterTestRequest(t))
	assert.ErrorIs(t, err, boom)
	assert.False(t, mr.Exists("tenant"))
}

func TestTPMColorMarker_NoRecordWithoutCollector(t *testing.T) {
	mr, client := newTPMTestRedis(t)
	e, err := NewTPMColorMarkerEngine(client, "tenant", []int{100}, "ns", &nextStubEngine{})
	require.NoError(t, err)

	resp, err := e.Process(newLimiterTestRequest(t))
	require.NoError(t, err)
	closeResponse(t, resp)
	assert.False(t, mr.Exists("tenant"))
}

func TestTPMColorMarker_RedisError(t *testing.T) {
	mr, client := newTPMTestRedis(t)
	next := &nextStubEngine{}
	e, err := NewTPMColorMarkerEngine(client, "tenant", []int{100}, "ns", next)
	require.NoError(t, err)
	mr.Close()

	_, err = e.Process(newLimiterTestRequest(t))
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 0, next.callCount)
}

// TestTPMColorMarker_RecordsReturnedAttemptOnly checks that only the usage of
// the response handed back is recorded, not that of a failed earlier attempt.
func TestTPMColorMarker_RecordsReturnedAttemptOnly(t *testing.T) {
	_, client := newTPMTestRedis(t)
	failed := usageEngine(1000, false)
	served := usageEngine(7, false)
	retry := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		resp, err := failed.Process(req)
		require.NoError(t, err)
		closeResponse(t, resp)
		return served.Process(req.WithContext(context.WithValue(req.Context(), contextKey("attempt"), 2)))
	})
	e, err := NewTPMColorMarkerEngine(client, "tenant", []int{100}, "ns", retry)
	require.NoError(t, err)

	resp, err := e.Process(newLimiterTestRequest(t))
	require.NoError(t, err)
	closeResponse(t, resp)
	assert.Equal(t, int64(7), mustReadTPM(t, client, "tenant"))
}
