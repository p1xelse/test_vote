// Package config читает TOML-конфиг сервиса, накладывает поверх переменные
// окружения и валидирует результат до того, как что-либо будет запущено.
package config

import (
	"fmt"
	"time"
)

// Config — полная конфигурация сервиса.
type Config struct {
	App      App      `toml:"app"`
	Admin    Admin    `toml:"admin"`
	Postgres Postgres `toml:"postgres"`
	Redis    Redis    `toml:"redis"`
	Vote     Vote     `toml:"vote"`
}

// App — общие параметры процесса.
type App struct {
	Env             string   `toml:"env" validate:"required,oneof=local staging prod"`
	HTTPAddr        string   `toml:"http_addr" validate:"required,notblank"`
	DebugAddr       string   `toml:"debug_addr" validate:"required,notblank"`
	ShutdownTimeout Duration `toml:"shutdown_timeout" validate:"required"`
	FrontendDir     string   `toml:"frontend_dir"`
	// InstanceID определяет, в какой шард счётчиков пишет эта реплика.
	// Пустой — сгенерируется при старте.
	InstanceID string `toml:"instance_id"`
}

// Admin — доступ к админским ручкам.
type Admin struct {
	Token string `toml:"token" validate:"required,notblank,min=8"`
}

// Postgres хранит опросы, варианты и результаты.
type Postgres struct {
	DSN            string   `toml:"dsn" validate:"required,notblank"`
	MaxConns       int32    `toml:"max_conns" validate:"required,min=1"`
	MinConns       int32    `toml:"min_conns" validate:"min=0"`
	ConnectTimeout Duration `toml:"connect_timeout" validate:"required"`
}

// Redis держит дедуп-ключи и горячие счётчики голосов.
type Redis struct {
	Addrs    []string `toml:"addrs" validate:"required,min=1,dive,notblank"`
	Username string   `toml:"username"`
	Password string   `toml:"password"`
}

// Vote — параметры горячего пути голосования.
type Vote struct {
	CookieName   string `toml:"cookie_name" validate:"required,notblank"`
	CookieSecret string `toml:"cookie_secret" validate:"required,min=16"`
	// DedupTTL — сколько живёт отметка «этот зритель уже голосовал».
	DedupTTL Duration `toml:"dedup_ttl" validate:"required"`
	// CounterShards — на сколько ключей размазаны счётчики одного опроса,
	// чтобы опрос не упирался в один слот Redis Cluster.
	CounterShards int `toml:"counter_shards" validate:"required,min=1,max=4096"`
	// CounterStripes — на сколько атомиков разбит один счётчик внутри процесса,
	// чтобы соседние горутины не дрались за одну кэш-линию.
	CounterStripes   int      `toml:"counter_stripes" validate:"required,min=1,max=1024"`
	FlushInterval    Duration `toml:"flush_interval" validate:"required"`
	SnapshotInterval Duration `toml:"snapshot_interval" validate:"required"`
	PollCacheTTL     Duration `toml:"poll_cache_ttl" validate:"required"`
	ResultsCacheTTL  Duration `toml:"results_cache_ttl" validate:"required"`
	RateLimitRPS     float64  `toml:"rate_limit_rps" validate:"min=0"`
	RateLimitBurst   int      `toml:"rate_limit_burst" validate:"min=0"`
}

// Duration — time.Duration, читаемый из TOML как строка вида "200ms".
type Duration time.Duration

// Std возвращает обычный time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalText реализует encoding.TextUnmarshaler для BurntSushi/toml.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", text, err)
	}
	*d = Duration(parsed)

	return nil
}
