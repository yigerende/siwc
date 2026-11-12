package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAccountRequestRPMCacheMultipleInstancesAndRetries(t *testing.T) {
	server := miniredis.RunT(t)
	server.SetTime(time.Unix(10000, 0))
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(service.AccountRequestRPMCache)
	ctx := context.Background()
	write := func(instance string, now, second, count int64) {
		require.NoError(t, cache.WriteAccountRequestRPM(ctx, instance, now, []service.AccountRequestRPMSnapshot{{AccountID: 1, Second: second, Count: count}}))
	}
	// Different process wall clocks are normalized to the Redis clock.
	write("a", 1000, 1000, 10)
	write("b", 90000, 90000, 7)
	write("a", 1000, 1000, 10) // Retry after a lost acknowledgement.
	write("a", 1000, 1000, 12) // New increments in the same second.
	write("a", 1000, 1000, 10) // A stale retry cannot decrease a count.
	counts, err := cache.GetAccountRequestRPMBatch(ctx, []int64{1, 2, 1})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{1: 19, 2: 0}, counts)
	server.SetTime(time.Unix(10059, 0))
	counts, err = cache.GetAccountRequestRPMBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, 19, counts[1])
	server.SetTime(time.Unix(10060, 0))
	counts, err = cache.GetAccountRequestRPMBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Zero(t, counts[1])
}

func TestAccountRequestRPMCacheRollingWindowAndBoundedStorage(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(service.AccountRequestRPMCache)
	ctx := context.Background()
	for second := int64(1000); second < 1180; second++ {
		server.SetTime(time.Unix(second, 0))
		require.NoError(t, cache.WriteAccountRequestRPM(ctx, "a", second, []service.AccountRequestRPMSnapshot{{AccountID: 5, Second: second, Count: 2}}))
	}
	counts, err := cache.GetAccountRequestRPMBatch(ctx, []int64{5})
	require.NoError(t, err)
	require.Equal(t, 120, counts[5])
	keys := accountRequestRPMKeys(5)
	require.EqualValues(t, 60, client.HLen(ctx, keys[0]).Val())
	require.EqualValues(t, 60, client.ZCard(ctx, keys[1]).Val())
	server.SetTime(time.Unix(1209, 0))
	counts, err = cache.GetAccountRequestRPMBatch(ctx, []int64{5})
	require.NoError(t, err)
	require.Equal(t, 60, counts[5])
	server.FastForward(121 * time.Second)
	require.Empty(t, server.Keys())
}

func TestAccountRequestRPMCacheDelayedFlushAndUnavailable(t *testing.T) {
	server := miniredis.RunT(t)
	server.SetTime(time.Unix(2000, 0))
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	cache := NewConcurrencyCache(client, 15, 900).(service.AccountRequestRPMCache)
	ctx := context.Background()
	require.NoError(t, cache.WriteAccountRequestRPM(ctx, "a", 1000, []service.AccountRequestRPMSnapshot{
		{AccountID: 1, Second: 941, Count: 3},
		{AccountID: 1, Second: 940, Count: 100}, // Already outside the window.
	}))
	counts, err := cache.GetAccountRequestRPMBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, 3, counts[1])
	server.SetTime(time.Unix(2001, 0))
	counts, err = cache.GetAccountRequestRPMBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Zero(t, counts[1])
	require.NoError(t, client.Close())
	counts, err = cache.GetAccountRequestRPMBatch(ctx, []int64{1})
	require.Error(t, err)
	require.Nil(t, counts, "an unavailable cache must not fabricate zero counts")
}
