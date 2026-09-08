// Package snapshot переносит счётчики голосования из Redis в PostgreSQL.
//
// Redis — быстрое, но не вечное хранилище: у ключей стоит TTL, а persistence
// у него отключена сознательно. Снапшоттер делает из горячих счётчиков
// долговременный результат и заодно ведёт историю, по которой видно, как
// голоса набегали в течение ролика.
package snapshot

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/dneumoin/test_vote/backend/internal/counters"
	"github.com/dneumoin/test_vote/backend/internal/observability"
)

// closedGrace — сколько снапшоттер продолжает следить за закрытым опросом.
// Нужен, чтобы финальные цифры гарантированно доехали в PostgreSQL после
// последнего сброса счётчиков.
const closedGrace = time.Hour

//go:generate mockgen -source=worker.go -destination=mocks/worker.go -package=mocks

// PollLister отдаёт опросы, за которыми нужно следить.
type PollLister interface {
	ListForSnapshot(ctx context.Context, closedGrace time.Duration) ([]uuid.UUID, error)
}

// LiveCounters читает горячие счётчики.
type LiveCounters interface {
	ReadCounts(ctx context.Context, pollID uuid.UUID) (counters.Snapshot, error)
}

// ResultsStorage сохраняет агрегаты.
type ResultsStorage interface {
	Save(ctx context.Context, pollID uuid.UUID, voters int64, counts map[uuid.UUID]int64) error
}

// Worker периодически снимает агрегаты и складывает их в PostgreSQL.
type Worker struct {
	polls    PollLister
	live     LiveCounters
	storage  ResultsStorage
	interval time.Duration
	logger   *slog.Logger
	metrics  *observability.Metrics

	// lastVoters помнит, на чём остановился прошлый снимок, чтобы не писать
	// в базу одну и ту же цифру каждые пару секунд.
	lastVoters map[uuid.UUID]int64
	lastRun    time.Time
}

// New создаёт снапшоттер.
func New(
	polls PollLister,
	live LiveCounters,
	storage ResultsStorage,
	interval time.Duration,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *Worker {
	return &Worker{
		polls:      polls,
		live:       live,
		storage:    storage,
		interval:   interval,
		logger:     logger,
		metrics:    metrics,
		lastVoters: make(map[uuid.UUID]int64),
	}
}

// Run крутит снятие снимков до отмены контекста.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.tick(ctx)
		case <-ctx.Done():
			// Последний снимок со своим дедлайном: без него результаты за
			// последние секунды опроса остались бы только в Redis.
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			w.tick(finalCtx)
			cancel()

			return
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	pollIDs, err := w.polls.ListForSnapshot(ctx, closedGrace)
	if err != nil {
		w.metrics.SnapshotErrors.Inc()
		w.logger.Error("list polls for snapshot", slog.Any("error", err))

		return
	}

	watched := make(map[uuid.UUID]struct{}, len(pollIDs))
	for _, pollID := range pollIDs {
		watched[pollID] = struct{}{}
		w.snapshotPoll(ctx, pollID)
	}

	// Опросы, выпавшие из наблюдения, забываем, чтобы карта не росла вечно.
	for pollID := range w.lastVoters {
		if _, ok := watched[pollID]; !ok {
			delete(w.lastVoters, pollID)
		}
	}

	w.lastRun = time.Now()
	w.metrics.SnapshotAgeSec.Set(0)
}

func (w *Worker) snapshotPoll(ctx context.Context, pollID uuid.UUID) {
	snapshot, err := w.live.ReadCounts(ctx, pollID)
	if err != nil {
		w.metrics.SnapshotErrors.Inc()
		w.logger.Error("read live counters", slog.String("poll_id", pollID.String()), slog.Any("error", err))

		return
	}

	if !snapshot.Found {
		return
	}

	if previous, ok := w.lastVoters[pollID]; ok && previous == snapshot.Voters {
		return
	}

	if err := w.storage.Save(ctx, pollID, snapshot.Voters, snapshot.Options); err != nil {
		w.metrics.SnapshotErrors.Inc()
		w.logger.Error("save results", slog.String("poll_id", pollID.String()), slog.Any("error", err))

		return
	}

	w.lastVoters[pollID] = snapshot.Voters
}

// Age возвращает время с последнего успешного прохода — для health-check.
func (w *Worker) Age() time.Duration {
	if w.lastRun.IsZero() {
		return 0
	}

	return time.Since(w.lastRun)
}
