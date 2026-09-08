// Package results собирает обезличенные результаты опроса.
//
// Пока опрос идёт, цифры берутся из живых счётчиков; когда те истекают —
// из агрегатов, которые снапшоттер сложил в PostgreSQL.
package results

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/dneumoin/test_vote/backend/internal/counters"
	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
	"github.com/dneumoin/test_vote/backend/internal/service/results/dto"
)

// maxTimelinePoints ограничивает выборку истории, чтобы админка не утянула
// всю таблицу снимков одним запросом.
const maxTimelinePoints = 500

//go:generate mockgen -source=service.go -destination=mocks/service.go -package=mocks

// PollProvider отдаёт определение опроса.
type PollProvider interface {
	Get(ctx context.Context, pollID uuid.UUID) (polldto.Poll, error)
}

// LiveCounters читает счётчики из горячего хранилища.
type LiveCounters interface {
	ReadCounts(ctx context.Context, pollID uuid.UUID) (counters.Snapshot, error)
}

// PersistedResults читает сохранённые агрегаты и их историю.
type PersistedResults interface {
	Load(ctx context.Context, pollID uuid.UUID) (int64, map[uuid.UUID]int64, error)
	Timeline(ctx context.Context, pollID uuid.UUID, limit int) ([]dto.TimelinePoint, error)
}

// Service отдаёт результаты опроса.
//
// Результаты кэшируются в памяти на пол-секунды: во время эфира их обновляют
// и админка, и страница голосования, и без кэша каждый такой запрос уходил бы
// в Redis за всеми шардами счётчиков.
type Service struct {
	polls     PollProvider
	live      LiveCounters
	persisted PersistedResults
	cacheTTL  time.Duration
	cache     sync.Map // uuid.UUID -> *cacheEntry
	group     singleflight.Group
	now       func() time.Time
}

type cacheEntry struct {
	results   dto.Results
	err       error
	expiresAt time.Time
}

// New создаёт сервис результатов.
func New(
	polls PollProvider,
	live LiveCounters,
	persisted PersistedResults,
	cacheTTL time.Duration,
) *Service {
	return &Service{polls: polls, live: live, persisted: persisted, cacheTTL: cacheTTL, now: time.Now}
}

// Get возвращает текущие результаты опроса.
func (s *Service) Get(ctx context.Context, pollID uuid.UUID) (dto.Results, error) {
	if cached, ok := s.cache.Load(pollID); ok {
		entry, _ := cached.(*cacheEntry)
		if s.now().Before(entry.expiresAt) {
			return entry.results, entry.err
		}
	}

	result, err, _ := s.group.Do(pollID.String(), func() (any, error) {
		results, buildErr := s.build(ctx, pollID)
		entry := &cacheEntry{results: results, err: buildErr, expiresAt: s.now().Add(s.cacheTTL)}
		s.cache.Store(pollID, entry)

		return entry, nil
	})
	if err != nil {
		return dto.Results{}, err
	}

	entry, _ := result.(*cacheEntry)

	return entry.results, entry.err
}

// Timeline возвращает историю результатов, от старых к свежим.
func (s *Service) Timeline(ctx context.Context, pollID uuid.UUID, limit int) ([]dto.TimelinePoint, error) {
	if limit <= 0 || limit > maxTimelinePoints {
		limit = maxTimelinePoints
	}

	// Проверяем существование опроса, чтобы на несуществующий id отдать 404,
	// а не пустой список.
	if _, err := s.polls.Get(ctx, pollID); err != nil {
		return nil, err
	}

	points, err := s.persisted.Timeline(ctx, pollID, limit)
	if err != nil {
		return nil, fmt.Errorf("load timeline: %w", err)
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].CapturedAt.Before(points[j].CapturedAt)
	})

	return points, nil
}

func (s *Service) build(ctx context.Context, pollID uuid.UUID) (dto.Results, error) {
	poll, err := s.polls.Get(ctx, pollID)
	if err != nil {
		return dto.Results{}, err
	}

	snapshot, err := s.live.ReadCounts(ctx, pollID)
	if err != nil {
		return dto.Results{}, fmt.Errorf("read live counters: %w", err)
	}

	source := dto.SourceLive
	voters, counts := snapshot.Voters, snapshot.Options

	if !snapshot.Found {
		// Счётчики истекли — значит опрос давно закончился и итог уже перенесён
		// в PostgreSQL снапшоттером.
		voters, counts, err = s.persisted.Load(ctx, pollID)
		if err != nil {
			return dto.Results{}, fmt.Errorf("load persisted results: %w", err)
		}

		source = dto.SourcePersisted
	}

	return dto.Results{
		PollID:     poll.ID,
		Title:      poll.Title,
		Question:   poll.Question,
		Kind:       poll.Kind,
		Status:     poll.Status,
		Voters:     voters,
		Options:    buildOptionResults(poll, counts, voters),
		Final:      poll.Status == polldto.StatusClosed,
		Source:     source,
		ObservedAt: s.now().UTC(),
	}, nil
}

func buildOptionResults(poll polldto.Poll, counts map[uuid.UUID]int64, voters int64) []dto.OptionResult {
	options := make([]dto.OptionResult, 0, len(poll.Options))

	for _, option := range poll.Options {
		votes := counts[option.ID]

		// Доля считается от числа проголосовавших, а не от суммы отметок:
		// в multiple_choice один человек ставит несколько галочек, и проценты
		// от суммы отметок ответили бы не на тот вопрос.
		var share float64
		if voters > 0 {
			share = float64(votes) / float64(voters)
		}

		options = append(options, dto.OptionResult{
			OptionID: option.ID,
			Position: option.Position,
			Text:     option.Text,
			Votes:    votes,
			Share:    share,
		})
	}

	return options
}
