package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestNew_nilRegisterer(t *testing.T) {
	t.Parallel()
	m, err := New(nil)
	require.NoError(t, err)
	require.NotPanics(t, func() {
		m.ObserveDecodedBytes(100)
		m.ObserveRequestSumBytes(200)
		m.ObserveHTTPFetchDuration(0)
		m.ObserveRequestFetchDuration(0)
		m.ObserveRequestCacheHitRatio(1)
		m.IncHTTPFetches()
		m.IncCacheHits()
		m.IncFullCacheHitRequests()
		m.IncRejectedDueToImageDownload()
	})
}

func TestNew_registers(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := New(reg)
	require.NoError(t, err)

	m.ObserveDecodedBytes(5000)
	m.ObserveRequestSumBytes(8000)
	m.ObserveRequestCacheHitRatio(0.5)
	m.IncHTTPFetches()
	m.IncCacheHits()
	m.IncFullCacheHitRequests()

	families, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)
}

func TestNew_duplicateRegister(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	_, err := New(reg)
	require.NoError(t, err)
	_, err = New(reg)
	require.Error(t, err)
}
