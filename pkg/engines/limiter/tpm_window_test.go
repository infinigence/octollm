package limiter

import (
	"context"
	"fmt"
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
	_, _, err = reserveTPM(context.Background(), client, []string{"k"}, []int64{1}, 1, "[test]")
	assert.Error(t, err)
}

func TestTPMWindow_ReserveSeesEarlierReservations(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	mr.SetTime(at(0))
	res, tpm := mustReserveTPM(t, client, []string{"k"}, []int64{100}, 60)
	require.NotNil(t, res)
	assert.Equal(t, []int64{0}, tpm)

	// A request admitted while the first is still in flight sees its reservation.
	mr.SetTime(at(1))
	res, tpm = mustReserveTPM(t, client, []string{"k"}, []int64{100}, 60)
	require.NotNil(t, res)
	assert.Equal(t, []int64{60}, tpm)

	// Once the reservations reach the cap, the next request is rejected and
	// reserves nothing.
	res, tpm = mustReserveTPM(t, client, []string{"k"}, []int64{100}, 60)
	assert.Nil(t, res)
	assert.Equal(t, []int64{120}, tpm)
	assert.Equal(t, "120", mr.HGet("k", "bucket:00"))
	assert.Equal(t, at(1).Unix(), mustInt64(t, mr.HGet("k", "last_sec")))
	assert.Equal(t, tpmKeyTTL, mr.TTL("k"))
}

func TestTPMWindow_ReserveAllOrNothing(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	mr.SetTime(at(0))
	mustRecordTPM(t, client, "b", 10)

	res, tpm := mustReserveTPM(t, client, []string{"a", "b"}, []int64{100, 10}, 5)
	assert.Nil(t, res)
	assert.Equal(t, []int64{0, 10}, tpm)
	assert.False(t, mr.Exists("a"))
	assert.Equal(t, int64(10), mustReadTPM(t, client, "b"))
}

func TestTPMWindow_ReserveNothingWritesNothing(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	mr.SetTime(at(7))
	res, _ := mustReserveTPM(t, client, []string{"a", "b"}, []int64{1, 1}, 0)
	require.NotNil(t, res)
	assert.Empty(t, mr.Keys())

	// The usage is then recorded in full once settled.
	mr.SetTime(at(12))
	res.settle(newLimiterTestRequest(t), 4)
	assert.Equal(t, "4", mr.HGet("a", "bucket:10"))
	assert.Equal(t, "4", mr.HGet("b", "bucket:10"))
}

func TestTPMWindow_SettleIntoCurrentSlice(t *testing.T) {
	tests := []struct {
		name   string
		actual int64
		// afterRollOut is the TPM once the reservation rolled out but the
		// correction has not.
		afterRollOut int64
	}{
		{"usage above estimate", 130, 30},
		{"usage below estimate", 40, 0},
		{"usage equals estimate", 100, 0},
		{"refund", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr, client := newTPMTestRedis(t)

			mr.SetTime(at(2))
			mustRecordTPM(t, client, "k", 7) // another request in the same slice
			res, _ := mustReserveTPM(t, client, []string{"k"}, []int64{1000}, 100)
			require.NotNil(t, res)

			mr.SetTime(at(40))
			res.settle(newLimiterTestRequest(t), tt.actual)
			assert.Equal(t, 7+tt.actual, mustReadTPM(t, client, "k"))
			assert.Equal(t, "107", mr.HGet("k", "bucket:00"))
			if tt.actual != 100 {
				assert.Equal(t, fmt.Sprint(tt.actual-100), mr.HGet("k", "bucket:40"))
			}

			assert.Equal(t, tt.afterRollOut, readTPMAt(t, mr, client, "k", at(60)))
			assert.Equal(t, int64(0), readTPMAt(t, mr, client, "k", at(100)))
		})
	}
}

func TestTPMWindow_RecordNeverTakesWindowBelowZero(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	mr.SetTime(at(0))
	mustRecordTPM(t, client, "k", 30)
	// A refund larger than the window only takes what the window holds.
	mr.SetTime(at(10))
	mustRecordTPM(t, client, "k", -100)
	assert.Equal(t, "-30", mr.HGet("k", "bucket:10"))
	assert.Equal(t, int64(0), mustReadTPM(t, client, "k"))

	mr.SetTime(at(20))
	mustRecordTPM(t, client, "k", 50)
	assert.Equal(t, int64(50), mustReadTPM(t, client, "k"))
}

func TestTPMWindow_NegativeSumReadsZero(t *testing.T) {
	mr, client := newTPMTestRedis(t)

	mr.SetTime(at(0))
	mustRecordTPM(t, client, "k", 100)
	mr.SetTime(at(30))
	mustRecordTPM(t, client, "k", -100)

	// Once the 100 rolls out, the slices sum to -100 until the refund does too.
	assert.Equal(t, int64(0), readTPMAt(t, mr, client, "k", at(60)))
	mustRecordTPM(t, client, "k", 40)
	assert.Equal(t, int64(0), readTPMAt(t, mr, client, "k", at(60)))
	assert.Equal(t, int64(40), readTPMAt(t, mr, client, "k", at(90)))
}

func mustReserveTPM(t *testing.T, client *redis.Client, keys []string, caps []int64, tokens int64) (*tpmReservation, []int64) {
	t.Helper()
	res, tpm, err := reserveTPM(context.Background(), client, keys, caps, tokens, "[test]")
	require.NoError(t, err)
	return res, tpm
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
