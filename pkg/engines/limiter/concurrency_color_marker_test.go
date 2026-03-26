package limiter

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func TestFilterIncreasingRates(t *testing.T) {
	tests := []struct {
		name          string
		in            []int
		wantOut       []int
		wantFiltered  bool
	}{
		{
			name:         "empty",
			in:           nil,
			wantOut:      nil,
			wantFiltered: false,
		},
		{
			name:         "single",
			in:           []int{10},
			wantOut:      []int{10},
			wantFiltered: false,
		},
		{
			name:         "all_strictly_increasing",
			in:           []int{1, 5, 10, 100},
			wantOut:      []int{1, 5, 10, 100},
			wantFiltered: false,
		},
		{
			name:         "stops_on_non_increasing",
			in:           []int{1, 5, 3, 10},
			wantOut:      []int{1, 5},
			wantFiltered: true,
		},
		{
			name:         "stops_on_equal",
			in:           []int{1, 2, 2, 3},
			wantOut:      []int{1, 2},
			wantFiltered: true,
		},
		{
			name:         "second_elem_not_greater",
			in:           []int{10, 5, 1},
			wantOut:      []int{10},
			wantFiltered: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, filtered := filterIncreasingRates(tt.in)
			assert.Equal(t, tt.wantOut, got)
			assert.Equal(t, tt.wantFiltered, filtered)
		})
	}
}

func newConcurrencyColorMarkerTestRequest(t *testing.T) *octollm.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	assert.NoError(t, err)
	return octollm.NewRequest(req, octollm.APIFormatUnknown)
}

type priorityCaptureEngine struct {
	ns           string
	lastPriority int
	callCount    int
	resp         *octollm.Response
}

func (p *priorityCaptureEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	p.callCount++
	key := contextKey(fmt.Sprintf("%s:%s", p.ns, ContextKeyPriority))
	if s, ok := req.Context().Value(key).(string); ok {
		var pr int
		_, _ = fmt.Sscanf(s, ContextValuePrefixPriority+"%d", &pr)
		p.lastPriority = pr
	} else {
		p.lastPriority = -999
	}
	if p.resp == nil {
		return &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("ok"), nil)}, nil
	}
	return p.resp, nil
}

func TestNewConcurrencyColorMarkerEngine_ValidationAndFiltering(t *testing.T) {
	next := &nextStubEngine{}

	// empty rates -> disabled (pass-through)
	e, err := NewConcurrencyColorMarkerEngine(nil, "k", nil, time.Second, "ns", next)
	assert.NoError(t, err)
	assert.Nil(t, e.rates)
	assert.Nil(t, e.acquireScript)
	assert.Nil(t, e.releaseScript)
	assert.Nil(t, e.renewScript)

	// timeout must be positive when enabled
	_, err = NewConcurrencyColorMarkerEngine(nil, "k", []int{1}, 0, "ns", next)
	assert.Error(t, err)

	// non-increasing rates get filtered (stop at first violation)
	e, err = NewConcurrencyColorMarkerEngine(nil, "k", []int{1, 3, 2, 10}, time.Second, "ns", next)
	assert.NoError(t, err)
	assert.Equal(t, []int{1, 3}, e.rates)
}

func TestConcurrencyColorMarker_PassThroughWhenDisabled(t *testing.T) {
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200}}
	e, err := NewConcurrencyColorMarkerEngine(nil, "k", nil, time.Second, "ns", next)
	assert.NoError(t, err)

	req := newConcurrencyColorMarkerTestRequest(t)
	resp, err := e.Process(req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 1, next.callCount)
}

func TestConcurrencyColorMarker_PriorityAssignmentAndExhaustion(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "marker-ns"
	next := &priorityCaptureEngine{ns: ns}

	// rates=[1,2] => first acquires tier0+tier1 => priority=1; second acquires tier1 only => priority=0; third denied
	e, err := NewConcurrencyColorMarkerEngine(client, "marker-key", []int{1, 2}, 10*time.Second, ns, next)
	assert.NoError(t, err)

	resp1, err := e.Process(newConcurrencyColorMarkerTestRequest(t))
	assert.NoError(t, err)
	assert.NotNil(t, resp1)
	assert.Equal(t, 1, next.lastPriority)

	// Keep slot held (do not close resp1 yet)
	resp2, err := e.Process(newConcurrencyColorMarkerTestRequest(t))
	assert.NoError(t, err)
	assert.NotNil(t, resp2)
	assert.Equal(t, 0, next.lastPriority)

	_, err = e.Process(newConcurrencyColorMarkerTestRequest(t))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)

	// cleanup: close bodies to trigger release and stop renew goroutines
	assert.NoError(t, resp1.Body.Close())
	assert.NoError(t, resp2.Body.Close())
}

