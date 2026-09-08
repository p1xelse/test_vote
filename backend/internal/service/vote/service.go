// Package vote — горячий путь голосования.
//
// На один голос приходится ровно один поход в Redis: проверка «этот зритель
// уже голосовал» совмещена с установкой отметки. Сам голос при этом никуда
// не отправляется — он прибавляется к счётчику в памяти процесса, который
// сбрасывается наружу пачкой несколько раз в секунду.
package vote

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/dneumoin/test_vote/backend/internal/counters"
	"github.com/dneumoin/test_vote/backend/internal/dedup"
	"github.com/dneumoin/test_vote/backend/internal/observability"
	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
	"github.com/dneumoin/test_vote/backend/internal/service/vote/dto"
)

// dedupGrace — насколько отметка о голосовании переживает конец опроса.
// Нужна на случай, если зритель дожал кнопку в последнюю секунду и повторил
// запрос уже после закрытия.
const dedupGrace = 15 * time.Minute

// minDedupTTL — нижняя граница жизни отметки, чтобы у почти закрытого опроса
// дедупликация не выключалась сама собой.
const minDedupTTL = time.Minute

//go:generate mockgen -source=service.go -destination=mocks/service.go -package=mocks

// PollProvider отдаёт определение опроса. За ним стоит кэш в памяти, поэтому
// вызов на горячем пути безопасен.
type PollProvider interface {
	Get(ctx context.Context, pollID uuid.UUID) (polldto.Poll, error)
}

// Marker хранит отметки о том, кто уже проголосовал.
type Marker interface {
	MarkVoted(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// Service принимает голоса.
type Service struct {
	polls      PollProvider
	marker     Marker
	aggregator *counters.Aggregator
	metrics    *observability.Metrics
	dedupTTL   time.Duration
	now        func() time.Time
}

// New создаёт сервис голосования.
func New(
	polls PollProvider,
	marker Marker,
	aggregator *counters.Aggregator,
	metrics *observability.Metrics,
	dedupTTL time.Duration,
) *Service {
	return &Service{
		polls:      polls,
		marker:     marker,
		aggregator: aggregator,
		metrics:    metrics,
		dedupTTL:   dedupTTL,
		now:        time.Now,
	}
}

// Cast учитывает голос зрителя.
func (s *Service) Cast(ctx context.Context, params dto.CastParams) (dto.CastResult, error) {
	start := s.now()
	defer func() { s.metrics.VoteDuration.Observe(time.Since(start).Seconds()) }()

	poll, err := s.polls.Get(ctx, params.PollID)
	if err != nil {
		return dto.CastResult{}, err
	}

	if !poll.AcceptsVotesAt(start) {
		s.metrics.VotesTotal.WithLabelValues("rejected").Inc()

		return dto.CastResult{}, dto.ErrPollNotAccepting
	}

	if err := validateChoices(poll, params.OptionIDs); err != nil {
		s.metrics.VotesTotal.WithLabelValues("rejected").Inc()

		return dto.CastResult{}, err
	}

	key := dedup.Key(poll.ID, params.Identity)

	dedupStart := s.now()
	first, err := s.marker.MarkVoted(ctx, key, s.dedupTTLFor(poll, start))
	s.metrics.DedupDuration.Observe(time.Since(dedupStart).Seconds())

	if err != nil {
		return dto.CastResult{}, fmt.Errorf("dedup check: %w", err)
	}

	if !first {
		s.metrics.VotesTotal.WithLabelValues("duplicate").Inc()

		return dto.CastResult{Outcome: dto.OutcomeDuplicate}, nil
	}

	// Отметка ставится раньше, чем голос попадает в счётчик. Если процесс
	// умрёт ровно между этими строками, зритель потеряет свой голос, но не
	// сможет проголосовать дважды. Обратный порядок дал бы обратный размен,
	// и для опроса «один человек — один голос» он хуже.
	s.aggregator.AddVote(poll.ID, params.OptionIDs)
	s.metrics.VotesTotal.WithLabelValues("accepted").Inc()

	return dto.CastResult{Outcome: dto.OutcomeAccepted}, nil
}

// dedupTTLFor подбирает время жизни отметки под конкретный опрос: держать
// сутки ключи опроса, который закончился минуту назад, — впустую занятая память.
func (s *Service) dedupTTLFor(poll polldto.Poll, now time.Time) time.Duration {
	if poll.ClosesAt == nil {
		return s.dedupTTL
	}

	ttl := poll.ClosesAt.Sub(now) + dedupGrace
	if ttl > s.dedupTTL {
		return s.dedupTTL
	}

	if ttl < minDedupTTL {
		return minDedupTTL
	}

	return ttl
}

func validateChoices(poll polldto.Poll, optionIDs []uuid.UUID) error {
	if len(optionIDs) == 0 {
		return fmt.Errorf("%w: at least one option required", dto.ErrChoiceCount)
	}

	limit := poll.MaxChoices
	if poll.Kind != polldto.KindMultipleChoice {
		limit = 1
	}

	if len(optionIDs) > limit {
		return fmt.Errorf("%w: at most %d option(s) allowed", dto.ErrChoiceCount, limit)
	}

	seen := make(map[uuid.UUID]struct{}, len(optionIDs))
	for _, optionID := range optionIDs {
		if _, duplicate := seen[optionID]; duplicate {
			return fmt.Errorf("%w: option %s repeated", dto.ErrChoiceCount, optionID)
		}
		seen[optionID] = struct{}{}

		if !poll.HasOption(optionID) {
			return fmt.Errorf("%w: %s", dto.ErrInvalidOption, optionID)
		}
	}

	return nil
}

// IsClientError отличает ошибки, за которые отвечает клиент, от наших.
func IsClientError(err error) bool {
	return errors.Is(err, dto.ErrPollNotAccepting) ||
		errors.Is(err, dto.ErrInvalidOption) ||
		errors.Is(err, dto.ErrChoiceCount)
}
