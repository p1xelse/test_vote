package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics — метрики, по которым видно здоровье горячего пути голосования.
type Metrics struct {
	registry *prometheus.Registry

	// VotesTotal разбит по исходу: accepted, duplicate, rejected.
	VotesTotal    *prometheus.CounterVec
	VoteDuration  prometheus.Histogram
	DedupDuration prometheus.Histogram

	// FlushedDeltas — сколько голосов доехало из памяти процесса в Redis.
	FlushedDeltas prometheus.Counter
	FlushErrors   prometheus.Counter
	// PendingDeltas — голоса, которые не удалось сбросить и мы вернули в агрегатор.
	PendingDeltas prometheus.Gauge

	SnapshotErrors prometheus.Counter
	SnapshotAgeSec prometheus.Gauge

	HTTPRequests *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec
}

// NewMetrics регистрирует метрики в собственном реестре.
func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		registry: registry,
		VotesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vote_votes_total",
			Help: "Голоса, разложенные по исходу обработки.",
		}, []string{"outcome"}),
		VoteDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "vote_cast_duration_seconds",
			Help:    "Полное время обработки голоса.",
			Buckets: prometheus.ExponentialBuckets(0.0002, 2, 14),
		}),
		DedupDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "vote_dedup_duration_seconds",
			Help:    "Время round-trip'а дедупликации в Redis.",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 14),
		}),
		FlushedDeltas: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vote_flushed_deltas_total",
			Help: "Голоса, сброшенные из памяти процесса в Redis.",
		}),
		FlushErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vote_flush_errors_total",
			Help: "Неудачные сбросы счётчиков в Redis.",
		}),
		PendingDeltas: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vote_pending_deltas",
			Help: "Голоса, возвращённые в агрегатор после неудачного сброса.",
		}),
		SnapshotErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vote_snapshot_errors_total",
			Help: "Неудачные переносы агрегатов из Redis в PostgreSQL.",
		}),
		SnapshotAgeSec: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vote_snapshot_age_seconds",
			Help: "Сколько секунд прошло с последнего успешного снапшота.",
		}),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP-запросы по маршруту и коду ответа.",
		}, []string{"route", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Время ответа по маршруту.",
			Buckets: prometheus.ExponentialBuckets(0.0002, 2, 14),
		}, []string{"route"}),
	}

	registry.MustRegister(
		m.VotesTotal,
		m.VoteDuration,
		m.DedupDuration,
		m.FlushedDeltas,
		m.FlushErrors,
		m.PendingDeltas,
		m.SnapshotErrors,
		m.SnapshotAgeSec,
		m.HTTPRequests,
		m.HTTPDuration,
	)

	return m
}

// Registry нужен, чтобы отдать метрики через promhttp.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }
