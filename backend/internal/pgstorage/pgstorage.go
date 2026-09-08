// Package pgstorage — тонкая обёртка над pgxpool: конфигурация пула,
// проверка связности и общий тип, который принимают все репозитории.
package pgstorage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB — пул соединений с PostgreSQL.
type DB struct {
	*pgxpool.Pool
}

// Options — параметры пула.
type Options struct {
	DSN            string
	MaxConns       int32
	MinConns       int32
	ConnectTimeout time.Duration
}

// New поднимает пул и сразу проверяет, что база отвечает: падать на старте
// лучше, чем принимать трафик и отдавать 500 на каждый запрос.
func New(ctx context.Context, opts Options) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	poolCfg.MaxConns = opts.MaxConns
	poolCfg.MinConns = opts.MinConns
	poolCfg.ConnConfig.ConnectTimeout = opts.ConnectTimeout
	poolCfg.MaxConnLifetime = time.Hour
	poolCfg.MaxConnIdleTime = 10 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()

		return nil, fmt.Errorf("ping: %w", err)
	}

	return &DB{Pool: pool}, nil
}
