package limiter

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tpmWindowStart is aligned to a minute, so offsets from it map to bucket names directly.
var tpmWindowStart = time.Unix(1_800_000_000-1_800_000_000%60, 0)

func newTPMTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	return mr, redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func at(sec int) time.Time {
	return tpmWindowStart.Add(time.Duration(sec) * time.Second)
}

func mustRecordTPM(t *testing.T, client *redis.Client, key string, tokens int64) {
	t.Helper()
	require.NoError(t, recordTPM(context.Background(), client, []string{key}, tokens))
}

func mustReadTPM(t *testing.T, client *redis.Client, key string) int64 {
	t.Helper()
	tpm, err := readTPM(context.Background(), client, []string{key})
	require.NoError(t, err)
	return tpm[0]
}

func TestTPMWindow_SlidingBuckets(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	mr.SetTime(at(0))
	mustRecordTPM(t, client, "k", 1)
	mr.SetTime(at(4))
	mustRecordTPM(t, client, "k", 10)
	mr.SetTime(at(5))
	mustRecordTPM(t, client, "k", 100)
	mr.SetTime(at(30))
	mustRecordTPM(t, client, "k", 1000)

	tests := []struct {
		name string
		sec  int
		want int64
	}{
		{"all slices in window", 30, 1111},
		{"first slice still in window", 59, 1111},
		{"first slice rolled out", 60, 1100},
		{"second slice rolled out", 65, 1000},
		{"last slice still in window", 89, 1000},
		{"everything rolled out", 90, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, readTPMAt(t, mr, client, "k", at(tt.sec)))
		})
	}
}

func TestTPMWindow_LongIdleClearsEveryBucket(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	for sec := 0; sec < 60; sec += 5 {
		mr.SetTime(at(sec))
		mustRecordTPM(t, client, "k", 1)
	}
	assert.Equal(t, int64(12), readTPMAt(t, mr, client, "k", at(59)))

	mr.SetTime(at(3600))
	mustRecordTPM(t, client, "k", 5)
	assert.Equal(t, int64(5), readTPMAt(t, mr, client, "k", at(3600)))
}

func TestTPMWindow_HashLayout(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	mr.SetTime(at(7))
	mustRecordTPM(t, client, "k", 3)
	mr.SetTime(at(59))
	mustRecordTPM(t, client, "k", 4)

	assert.Equal(t, "3", mr.HGet("k", "bucket:05"))
	assert.Equal(t, "4", mr.HGet("k", "bucket:55"))
	assert.Equal(t, at(59).Unix(), mustInt64(t, mr.HGet("k", "last_sec")))
	assert.Equal(t, tpmKeyTTL, mr.TTL("k"))
}

func TestTPMWindow_ClockBehindLastSecUsesLastSec(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	mr.SetTime(at(100))
	mustRecordTPM(t, client, "k", 5)
	// Should the Redis clock step back by over a minute (e.g. a failover to a
	// lagging replica), a slice that has since been reused must not be written
	// into, nor last_sec moved backwards.
	mr.SetTime(at(30))
	mustRecordTPM(t, client, "k", 7)

	assert.Equal(t, int64(12), readTPMAt(t, mr, client, "k", at(100)))
	assert.Equal(t, "12", mr.HGet("k", "bucket:40"))
	assert.Equal(t, at(100).Unix(), mustInt64(t, mr.HGet("k", "last_sec")))
}

func TestTPMWindow_ReadDoesNotCreateKey(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	tpm, err := readTPM(context.Background(), client, []string{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, []int64{0, 0}, tpm)
	assert.False(t, mr.Exists("a"))
	assert.False(t, mr.Exists("b"))
}

func TestTPMWindow_MultipleKeys(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	mr.SetTime(at(0))
	require.NoError(t, recordTPM(context.Background(), client, []string{"a", "b"}, 4))
	mr.SetTime(at(1))
	mustRecordTPM(t, client, "b", 6)

	mr.SetTime(at(2))
	tpm, err := readTPM(context.Background(), client, []string{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, []int64{4, 10}, tpm)
}

func TestTPMWindow_RedisError(t *testing.T) {
	mr, client := newTPMTestRedis(t)
	mr.Close()

	_, err := readTPM(context.Background(), client, []string{"k"})
	assert.Error(t, err)
	assert.Error(t, recordTPM(context.Background(), client, []string{"k"}, 1))
}

// readTPMAt reads key with the Redis clock set to now.
func readTPMAt(t *testing.T, mr *miniredis.Miniredis, client *redis.Client, key string, now time.Time) int64 {
	t.Helper()
	mr.SetTime(now)
	return mustReadTPM(t, client, key)
}

func mustInt64(t *testing.T, s string) int64 {
	t.Helper()
	v, err := parseInt64(s)
	require.NoError(t, err)
	return v
}
