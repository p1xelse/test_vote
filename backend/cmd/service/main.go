// Команда service — HTTP-сервис анонимных опросов.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/google/uuid"

	"github.com/dneumoin/test_vote/backend/db"
	"github.com/dneumoin/test_vote/backend/internal/config"
	"github.com/dneumoin/test_vote/backend/internal/counters"
	"github.com/dneumoin/test_vote/backend/internal/dedup"
	httpapi "github.com/dneumoin/test_vote/backend/internal/http"
	"github.com/dneumoin/test_vote/backend/internal/http/middleware"
	"github.com/dneumoin/test_vote/backend/internal/http/voter"
	"github.com/dneumoin/test_vote/backend/internal/observability"
	"github.com/dneumoin/test_vote/backend/internal/pgstorage"
	"github.com/dneumoin/test_vote/backend/internal/redisstorage"
	"github.com/dneumoin/test_vote/backend/internal/service/poll"
	pollstorage "github.com/dneumoin/test_vote/backend/internal/service/poll/storage"
	"github.com/dneumoin/test_vote/backend/internal/service/results"
	resultsstorage "github.com/dneumoin/test_vote/backend/internal/service/results/storage"
	"github.com/dneumoin/test_vote/backend/internal/service/vote"
	votestorage "github.com/dneumoin/test_vote/backend/internal/service/vote/storage"
	"github.com/dneumoin/test_vote/backend/internal/snapshot"
)

const defaultConfigPath = "configs/config.local.toml"

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "service failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", configPathFromEnv(), "путь к TOML-конфигу")
	migrateOnly := flag.Bool("migrate-only", false, "накатить миграции и выйти")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := observability.NewLogger(cfg.App.Env)

	// Контекст живёт до SIGINT/SIGTERM: по нему начинается штатное завершение.
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if err := db.Migrate(ctx, cfg.Postgres.DSN); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	if *migrateOnly {
		logger.Info("migrations applied")

		return nil
	}

	pg, err := pgstorage.New(ctx, pgstorage.Options{
		DSN:            cfg.Postgres.DSN,
		MaxConns:       cfg.Postgres.MaxConns,
		MinConns:       cfg.Postgres.MinConns,
		ConnectTimeout: cfg.Postgres.ConnectTimeout.Std(),
	})
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pg.Close()

	redisClient, err := redisstorage.New(ctx, redisstorage.Options{
		Addrs:    cfg.Redis.Addrs,
		Username: cfg.Redis.Username,
		Password: cfg.Redis.Password,
	})
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer redisClient.Close()

	instanceID := resolveInstanceID(cfg.App.InstanceID)
	metrics := observability.NewMetrics()

	logger = logger.With(slog.String("instance_id", instanceID))
	logger.Info("starting", slog.String("env", cfg.App.Env), slog.String("addr", cfg.App.HTTPAddr))

	// Опросы: PostgreSQL плюс кэш определения в памяти.
	pollService := poll.New(pollstorage.New(pg), cfg.Vote.PollCacheTTL.Std())

	// Голоса: дедупликация и счётчики в Redis, накопление — в памяти процесса.
	voteStore := votestorage.New(redisClient, votestorage.Options{
		Shards:     cfg.Vote.CounterShards,
		InstanceID: instanceID,
	})
	aggregator := counters.NewAggregator(cfg.Vote.CounterStripes)
	voteService := vote.New(pollService, voteStore, aggregator, metrics, cfg.Vote.DedupTTL.Std())
	flusher := counters.NewFlusher(aggregator, voteStore, cfg.Vote.FlushInterval.Std(), logger, metrics)

	// Результаты: живые счётчики из Redis, сохранённый итог — из PostgreSQL.
	resultsStore := resultsstorage.New(pg)
	resultsService := results.New(pollService, voteStore, resultsStore, cfg.Vote.ResultsCacheTTL.Std())
	snapshotter := snapshot.New(
		pollService, voteStore, resultsStore, cfg.Vote.SnapshotInterval.Std(), logger, metrics,
	)

	voterResolver := voter.New(
		dedup.NewSigner(cfg.Vote.CookieSecret),
		cfg.Vote.CookieName,
		cfg.Vote.DedupTTL.Std(),
		cfg.App.Env != "local",
	)
	limiter := middleware.NewRateLimiter(cfg.Vote.RateLimitRPS, cfg.Vote.RateLimitBurst)

	server := httpapi.New(httpapi.Deps{
		Polls:       pollService,
		PollsAdmin:  pollService,
		Votes:       voteService,
		Results:     resultsService,
		Voters:      voterResolver,
		RateLimiter: limiter,
		Metrics:     metrics,
		Logger:      logger,
		AdminToken:  cfg.Admin.Token,
		FrontendDir: cfg.App.FrontendDir,
	})
	debugServer := httpapi.NewDebugServer(metrics)

	workerCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	var workers sync.WaitGroup

	workers.Add(3)
	go func() { defer workers.Done(); flusher.Run(workerCtx) }()
	go func() { defer workers.Done(); snapshotter.Run(workerCtx) }()
	go func() { defer workers.Done(); limiter.RunCleanup(workerCtx) }()

	serverErrors := make(chan error, 2)
	go func() { serverErrors <- listen(server.Start(cfg.App.HTTPAddr)) }()
	go func() { serverErrors <- listen(debugServer.Start(cfg.App.DebugAddr)) }()

	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case runErr = <-serverErrors:
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(
		context.WithoutCancel(ctx), cfg.App.ShutdownTimeout.Std(),
	)
	defer cancelShutdown()

	// Порядок важен: сначала сервер перестаёт принимать запросы и дорабатывает
	// уже принятые, и только потом останавливаются воркеры. Иначе голоса,
	// пришедшие после финального сброса счётчиков, никуда бы не доехали.
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown http server", slog.Any("error", err))
	}

	if err := debugServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown debug server", slog.Any("error", err))
	}

	stopWorkers()
	workers.Wait()
	logger.Info("stopped")

	return runErr
}

// listen прячет штатное завершение сервера, чтобы оно не выглядело ошибкой.
func listen(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

func configPathFromEnv() string {
	if path := os.Getenv("CONFIG_PATH"); path != "" {
		return path
	}

	return defaultConfigPath
}

// resolveInstanceID даёт реплике устойчивый идентификатор: от него зависит,
// в какой шард счётчиков она пишет.
func resolveInstanceID(configured string) string {
	if configured != "" {
		return configured
	}

	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		return hostname
	}

	return uuid.NewString()
}
