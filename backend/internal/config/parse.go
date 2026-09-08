package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/go-playground/validator/v10"
	"github.com/go-playground/validator/v10/non-standard/validators"
)

var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// Отдельный тег для строк из пробелов: "required" их пропускает.
	if err := v.RegisterValidation("notblank", validators.NotBlank); err != nil {
		panic(fmt.Sprintf("register notblank validation: %v", err))
	}

	return v
}

// Load читает TOML-файл, накладывает переменные окружения и валидирует итог.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("decode config file %q: %w", path, err)
	}

	if err := applyEnv(cfg); err != nil {
		return nil, fmt.Errorf("apply env overrides: %w", err)
	}

	if err := validate.Struct(cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

// applyEnv позволяет переопределить всё, что различается между локальным
// запуском, docker-compose и «продом», не таская отдельный файл на каждое окружение.
func applyEnv(cfg *Config) error {
	envString("APP_ENV", &cfg.App.Env)
	envString("HTTP_ADDR", &cfg.App.HTTPAddr)
	envString("DEBUG_ADDR", &cfg.App.DebugAddr)
	envString("FRONTEND_DIR", &cfg.App.FrontendDir)
	envString("INSTANCE_ID", &cfg.App.InstanceID)
	envString("ADMIN_TOKEN", &cfg.Admin.Token)
	envString("POSTGRES_DSN", &cfg.Postgres.DSN)
	envString("REDIS_USERNAME", &cfg.Redis.Username)
	envString("REDIS_PASSWORD", &cfg.Redis.Password)
	envString("COOKIE_SECRET", &cfg.Vote.CookieSecret)

	if raw, ok := os.LookupEnv("REDIS_ADDRS"); ok {
		addrs := strings.Split(raw, ",")
		for i := range addrs {
			addrs[i] = strings.TrimSpace(addrs[i])
		}
		cfg.Redis.Addrs = addrs
	}

	if raw, ok := os.LookupEnv("POSTGRES_MAX_CONNS"); ok {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return fmt.Errorf("parse POSTGRES_MAX_CONNS: %w", err)
		}
		cfg.Postgres.MaxConns = int32(parsed)
	}

	// Нагрузочный тест идёт с одного адреса, поэтому лимитер на время замера
	// выключают: RATE_LIMIT_RPS=0.
	if raw, ok := os.LookupEnv("RATE_LIMIT_RPS"); ok {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("parse RATE_LIMIT_RPS: %w", err)
		}
		cfg.Vote.RateLimitRPS = parsed
	}

	return nil
}

func envString(key string, dst *string) {
	if value, ok := os.LookupEnv(key); ok {
		*dst = value
	}
}
