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
			name:         "all_non_decreasing",
			in:           []int{1, 5, 10, 100},
			wantOut:      []int{1, 5, 10, 100},
			wantFiltered: false,
		},
		{
			name:         "stops_on_decrease",
			in:           []int{1, 5, 3, 10},
			wantOut:      []int{1, 5},
			wantFiltered: true,
		},
		{
			name:         "allows_equal_plateau",
			in:           []int{1, 2, 2, 3},
			wantOut:      []int{1, 2, 2, 3},
			wantFiltered: false,
		},
		{
			name:         "stops_after_plateau_on_decrease",
			in:           []int{1, 2, 2, 1},
			wantOut:      []int{1, 2, 2},
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

func TestNormalizeConcurrencyMarkerRates(t *testing.T) {
	tests := []struct {
		name       string
		in         []int
		wantOut    []int
		wantAltered bool
	}{
		{name: "empty", in: nil, wantOut: nil, wantAltered: false},
		{name: "all_zero_kept", in: []int{0, 0}, wantOut: []int{0, 0}, wantAltered: false},
		{name: "leading_zeros_kept", in: []int{0, 0, 3, 2, 1}, wantOut: []int{0, 0, 3, 2, 1}, wantAltered: false},
		{name: "negative_as_zero", in: []int{-1, 3}, wantOut: []int{0, 3}, wantAltered: true},
		{name: "middle_zeros_kept", in: []int{3, 0, 0}, wantOut: []int{3, 0, 0}, wantAltered: false},
		{name: "arbitrary_order_ok", in: []int{1, 3, 2, 10}, wantOut: []int{1, 3, 2, 10}, wantAltered: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, altered := normalizeConcurrencyMarkerRates(tt.in)
			assert.Equal(t, tt.wantOut, got)
			assert.Equal(t, tt.wantAltered, altered)
		})
	}
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

	// non-monotonic values are kept (independent per-tier caps)
	e, err = NewConcurrencyColorMarkerEngine(nil, "k", []int{1, 3, 2, 10}, time.Second, "ns", next)
	assert.NoError(t, err)
	assert.Equal(t, []int{1, 3, 2, 10}, e.rates)

	// only zeros / negatives -> enabled; every tier has limit 0 so nothing is admitted
	e, err = NewConcurrencyColorMarkerEngine(nil, "k", []int{0, 0, -3}, time.Second, "ns", next)
	assert.NoError(t, err)
	assert.Equal(t, []int{0, 0, 0}, e.rates)
	assert.NotNil(t, e.acquireScript)
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

	// rates=[1,1] => two independent slots (p1 then p0); third denied
	e, err := NewConcurrencyColorMarkerEngine(client, "marker-key", []int{1, 1}, 10*time.Second, ns, next)
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

// [3,0]: three slots at highest priority only; fourth rejected.
func TestConcurrencyColorMarker_EqualTierLimits_AllPriority1(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "marker-ns-eq33"
	next := &priorityCaptureEngine{ns: ns}

	e, err := NewConcurrencyColorMarkerEngine(client, "marker-key-eq33", []int{3, 0}, 10*time.Second, ns, next)
	assert.NoError(t, err)

	var resps []*octollm.Response
	for i := 0; i < 3; i++ {
		resp, err := e.Process(newConcurrencyColorMarkerTestRequest(t))
		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Equal(t, 1, next.lastPriority, "request %d should be colored priority 1", i+1)
		resps = append(resps, resp)
	}

	_, err = e.Process(newConcurrencyColorMarkerTestRequest(t))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 3, next.callCount, "denied request must not reach next")

	for _, r := range resps {
		assert.NoError(t, r.Body.Close())
	}
}

func TestConcurrencyColorMarker_Bands321And300(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "marker-ns-bands"
	next := &priorityCaptureEngine{ns: ns}

	e, err := NewConcurrencyColorMarkerEngine(client, "marker-key-bands", []int{3, 2, 1}, 10*time.Second, ns, next)
	assert.NoError(t, err)

	want := []int{2, 2, 2, 1, 1, 0}
	var resps []*octollm.Response
	for i, pr := range want {
		resp, err := e.Process(newConcurrencyColorMarkerTestRequest(t))
		assert.NoError(t, err, "slot %d", i+1)
		assert.Equal(t, pr, next.lastPriority)
		resps = append(resps, resp)
	}
	_, err = e.Process(newConcurrencyColorMarkerTestRequest(t))
	assert.ErrorIs(t, err, ErrRateLimitReached)
	for _, r := range resps {
		assert.NoError(t, r.Body.Close())
	}

	next2 := &priorityCaptureEngine{ns: ns}
	e2, err := NewConcurrencyColorMarkerEngine(client, "marker-key-300", []int{3, 0, 0}, 10*time.Second, ns, next2)
	assert.NoError(t, err)
	var resps2 []*octollm.Response
	for i := 0; i < 3; i++ {
		resp, err := e2.Process(newConcurrencyColorMarkerTestRequest(t))
		assert.NoError(t, err)
		assert.Equal(t, 2, next2.lastPriority, "req %d", i+1)
		resps2 = append(resps2, resp)
	}
	_, err = e2.Process(newConcurrencyColorMarkerTestRequest(t))
	assert.ErrorIs(t, err, ErrRateLimitReached)
	for _, r := range resps2 {
		assert.NoError(t, r.Body.Close())
	}
}

