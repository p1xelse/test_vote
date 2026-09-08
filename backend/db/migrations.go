// Package db хранит SQL-миграции, вшитые в бинарь, и умеет их применять.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	// Регистрирует драйвер "pgx" для database/sql, которого требует goose.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate накатывает миграции на базу из DSN.
//
// Миграции лежат внутри бинаря, поэтому отдельный CLI-инструмент и его версия
// в образе не нужны: `--migrate-only` делает ровно то же самое.
func Migrate(ctx context.Context, dsn string) error {
	goose.SetBaseFS(migrationsFS)

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	return nil
}
