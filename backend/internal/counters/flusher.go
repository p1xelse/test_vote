package counters

import (
	"context"
	"log/slog"
	"time"

	"github.com/dneumoin/test_vote/backend/internal/observability"
)

// Sink — внешнее хранилище счётчиков, куда уезжают накопленные дельты.
type Sink interface {
	FlushDeltas(ctx context.Context, deltas []Delta) error
}

// Flusher периодически перекладывает накопленное из агрегатора в Sink.
type Flusher struct {
	aggregator *Aggregator
	sink       Sink
	interval   time.Duration
	logger     *slog.Logger
	metrics    *observability.Metrics
}

// NewFlusher создаёт сбрасыватель счётчиков.
func NewFlusher(
	aggregator *Aggregator,
	sink Sink,
	interval time.Duration,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *Flusher {
	return &Flusher{
		aggregator: aggregator,
		sink:       sink,
		interval:   interval,
		logger:     logger,
		metrics:    metrics,
	}
}

// Run крутит сброс до отмены контекста, после чего делает финальный сброс.
func (f *Flusher) Run(ctx context.Context) {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			f.flush(ctx)
		case <-ctx.Done():
			// Контекст уже отменён, поэтому финальный сброс идёт со своим
			// дедлайном — иначе последние голоса гарантированно потерялись бы.
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			f.flush(finalCtx)
			cancel()

			return
		}
	}
}

func (f *Flusher) flush(ctx context.Context) {
	deltas := f.aggregator.Drain()
	if len(deltas) == 0 {
		return
	}

	var total int64
	for _, delta := range deltas {
		total += delta.Total()
	}

	if err := f.sink.FlushDeltas(ctx, deltas); err != nil {
		// Возвращаем голоса в агрегатор: следующая попытка сбросит их вместе
		// со свежими. Потерять их можно только вместе с самой репликой.
		f.aggregator.Restore(deltas)
		f.metrics.FlushErrors.Inc()
		f.metrics.PendingDeltas.Add(float64(total))
		f.logger.Error("flush vote counters", slog.Any("error", err), slog.Int64("votes", total))

		return
	}

	f.metrics.FlushedDeltas.Add(float64(total))
	f.metrics.PendingDeltas.Set(0)
}
