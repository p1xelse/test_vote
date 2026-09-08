package middleware_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dneumoin/test_vote/backend/internal/http/middleware"
)

func TestRateLimiterAllowsBurstThenBlocks(t *testing.T) {
	t.Parallel()

	limiter := middleware.NewRateLimiter(1, 3)

	for i := range 3 {
		require.True(t, limiter.Allow("203.0.113.1"), "запрос %d должен пройти в пределах burst", i)
	}

	require.False(t, limiter.Allow("203.0.113.1"), "burst исчерпан")
}

func TestRateLimiterIsolatesKeys(t *testing.T) {
	t.Parallel()

	limiter := middleware.NewRateLimiter(1, 1)

	require.True(t, limiter.Allow("203.0.113.1"))
	require.False(t, limiter.Allow("203.0.113.1"))
	// Один назойливый адрес не должен мешать остальным зрителям.
	require.True(t, limiter.Allow("203.0.113.2"))
}

func TestRateLimiterIsRaceFree(t *testing.T) {
	t.Parallel()

	limiter := middleware.NewRateLimiter(1000, 1000)

	var wg sync.WaitGroup
	wg.Add(32)
	for worker := range 32 {
		go func() {
			defer wg.Done()

			for i := range 200 {
				limiter.Allow("10.0." + strconv.Itoa(worker) + "." + strconv.Itoa(i%256))
			}
		}()
	}
	wg.Wait()
}

func BenchmarkRateLimiterAllow(b *testing.B) {
	limiter := middleware.NewRateLimiter(1e9, 1e9)

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			limiter.Allow("203.0.113." + strconv.Itoa(i%256))
		}
	})
}
