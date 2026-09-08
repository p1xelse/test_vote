//go:build integration

// Package boilerplate поднимает реальные PostgreSQL и Redis для
// интеграционных тестов.
//
// Пакет собирается только с тегом integration, поэтому обычный `go test ./...`
// его не видит и не требует поднятой инфраструктуры.
package boilerplate

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/dneumoin/test_vote/backend/db"
	"github.com/dneumoin/test_vote/backend/internal/pgstorage"
	"github.com/dneumoin/test_vote/backend/internal/redisstorage"
)

// Значения по умолчанию совпадают с docker-compose.yml, поэтому после
// `make up` тесты запускаются без единой переменной окружения.
const (
	//nolint:gosec // Локальный docker-compose, не секрет.
	defaultPostgresDSN = "postgres://vote:vote@localhost:5432/vote?sslmode=disable"
	defaultRedisAddr   = "localhost:6379"
)

// PostgresDSN возвращает строку подключения к тестовой базе.
func PostgresDSN() string {
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}

	return defaultPostgresDSN
}

// RedisAddr возвращает адрес тестового Redis.
func RedisAddr() string {
	if addr := os.Getenv("TEST_REDIS_ADDR"); addr != "" {
		return addr
	}

	return defaultRedisAddr
}

// NewPostgres подключается к тестовой базе и накатывает миграции.
func NewPostgres(t *testing.T) *pgstorage.DB {
	t.Helper()

	ctx := context.Background()
	require.NoError(t, db.Migrate(ctx, PostgresDSN()), "не удалось применить миграции; поднят ли `make up`?")

	pool, err := pgstorage.New(ctx, pgstorage.Options{
		DSN:            PostgresDSN(),
		MaxConns:       8,
		MinConns:       1,
		ConnectTimeout: 5 * time.Second,
	})
	require.NoError(t, err, "не удалось подключиться к PostgreSQL; поднят ли `make up`?")

	t.Cleanup(pool.Close)

	return pool
}

// NewRedis подключается к тестовому Redis.
func NewRedis(t *testing.T) rueidis.Client {
	t.Helper()

	client, err := redisstorage.New(context.Background(), redisstorage.Options{Addrs: []string{RedisAddr()}})
	require.NoError(t, err, "не удалось подключиться к Redis; поднят ли `make up`?")

	t.Cleanup(client.Close)

	return client
}
