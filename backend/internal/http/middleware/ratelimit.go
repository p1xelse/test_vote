// Package middleware — общие HTTP-прослойки сервиса.
package middleware

import (
	"context"
	"hash/fnv"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/dneumoin/test_vote/backend/internal/http/apierr"
)

// rateLimiterShards — на сколько независимых карт разложен лимитер.
//
// Готовый лимитер из echo держит всех посетителей в одной карте под общим
// мьютексом. На десятках тысяч запросов в секунду этот мьютекс становится
// узким местом раньше, чем что-либо ещё в сервисе, поэтому здесь IP-адреса
// разложены по шардам и каждый живёт под своей блокировкой.
const rateLimiterShards = 256

// bucketTTL — через сколько простоя корзина посетителя выбрасывается.
const bucketTTL = 5 * time.Minute

type bucket struct {
	tokens float64
	seenAt time.Time
}

type limiterShard struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

// RateLimiter — token bucket на адрес, шардированный по ключу.
//
// Это грубая защита от скрипта в цикле, а не дедупликация: за общим NAT
// лимит общий на всех, поэтому он выставлен с большим запасом.
type RateLimiter struct {
	shards [rateLimiterShards]*limiterShard
	rps    float64
	burst  float64
	now    func() time.Time
}

// NewRateLimiter создаёт лимитер на rps запросов в секунду с burst в запасе.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	limiter := &RateLimiter{rps: rps, burst: float64(burst), now: time.Now}
	for i := range limiter.shards {
		limiter.shards[i] = &limiterShard{buckets: make(map[string]*bucket)}
	}

	return limiter
}

// Allow сообщает, пропускать ли запрос от этого ключа.
func (l *RateLimiter) Allow(key string) bool {
	shard := l.shardFor(key)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	now := l.now()

	current, ok := shard.buckets[key]
	if !ok {
		shard.buckets[key] = &bucket{tokens: l.burst - 1, seenAt: now}

		return true
	}

	current.tokens += now.Sub(current.seenAt).Seconds() * l.rps
	if current.tokens > l.burst {
		current.tokens = l.burst
	}
	current.seenAt = now

	if current.tokens < 1 {
		return false
	}

	current.tokens--

	return true
}

// Middleware ограничивает частоту запросов по IP.
//
// При rps <= 0 прослойка становится сквозной. Это нужно для нагрузочных
// тестов: весь трафик там идёт с одного адреса, и лимитер померил бы сам себя,
// а не сервис.
func (l *RateLimiter) Middleware() echo.MiddlewareFunc {
	if l.rps <= 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !l.Allow(c.RealIP()) {
				return apierr.New(http.StatusTooManyRequests, apierr.CodeRateLimited, "too many requests")
			}

			return next(c)
		}
	}
}

// RunCleanup удаляет корзины, к которым давно не обращались: без этого карта
// росла бы по числу уникальных зрителей, а их за опрос десятки миллионов.
func (l *RateLimiter) RunCleanup(ctx context.Context) {
	ticker := time.NewTicker(bucketTTL)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			deadline := l.now().Add(-bucketTTL)
			for _, shard := range &l.shards {
				shard.mu.Lock()
				for key, current := range shard.buckets {
					if current.seenAt.Before(deadline) {
						delete(shard.buckets, key)
					}
				}
				shard.mu.Unlock()
			}
		case <-ctx.Done():
			return
		}
	}
}

func (l *RateLimiter) shardFor(key string) *limiterShard {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(key))

	return l.shards[hash.Sum32()%rateLimiterShards]
}
