package results_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/dneumoin/test_vote/backend/internal/counters"
	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
	"github.com/dneumoin/test_vote/backend/internal/service/results"
	"github.com/dneumoin/test_vote/backend/internal/service/results/dto"
	"github.com/dneumoin/test_vote/backend/internal/service/results/mocks"
)

type fixture struct {
	service   *results.Service
	polls     *mocks.MockPollProvider
	live      *mocks.MockLiveCounters
	persisted *mocks.MockPersistedResults
}

func newFixture(t *testing.T, cacheTTL time.Duration) *fixture {
	t.Helper()

	ctrl := gomock.NewController(t)
	f := &fixture{
		polls:     mocks.NewMockPollProvider(ctrl),
		live:      mocks.NewMockLiveCounters(ctrl),
		persisted: mocks.NewMockPersistedResults(ctrl),
	}
	f.service = results.New(f.polls, f.live, f.persisted, cacheTTL)

	return f
}

func pollWithOptions(status polldto.Status, kind polldto.Kind, count int) polldto.Poll {
	poll := polldto.Poll{ID: uuid.New(), Title: "Эфир", Question: "Кто?", Kind: kind, Status: status}
	for i := range count {
		poll.Options = append(poll.Options, polldto.Option{
			ID: uuid.New(), Position: i, Text: string(rune('А' + i)),
		})
	}

	return poll
}

func TestGetUsesLiveCounters(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 0)
	poll := pollWithOptions(polldto.StatusActive, polldto.KindSingleChoice, 2)

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
	f.live.EXPECT().ReadCounts(gomock.Any(), poll.ID).Return(counters.Snapshot{
		Voters:  10,
		Options: map[uuid.UUID]int64{poll.Options[0].ID: 7, poll.Options[1].ID: 3},
		Found:   true,
	}, nil)

	got, err := f.service.Get(context.Background(), poll.ID)
	require.NoError(t, err)
	require.Equal(t, dto.SourceLive, got.Source)
	require.Equal(t, int64(10), got.Voters)
	require.False(t, got.Final)
	require.InDelta(t, 0.7, got.Options[0].Share, 1e-9)
	require.InDelta(t, 0.3, got.Options[1].Share, 1e-9)
}

func TestGetFallsBackToPersistedWhenCountersExpired(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 0)
	poll := pollWithOptions(polldto.StatusClosed, polldto.KindSingleChoice, 2)

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
	// Ключи в Redis истекли — итог берётся из того, что сохранил снапшоттер.
	f.live.EXPECT().ReadCounts(gomock.Any(), poll.ID).Return(counters.Snapshot{Found: false}, nil)
	f.persisted.EXPECT().Load(gomock.Any(), poll.ID).Return(
		int64(4), map[uuid.UUID]int64{poll.Options[0].ID: 4}, nil,
	)

	got, err := f.service.Get(context.Background(), poll.ID)
	require.NoError(t, err)
	require.Equal(t, dto.SourcePersisted, got.Source)
	require.Equal(t, int64(4), got.Voters)
	require.True(t, got.Final, "закрытый опрос отдаёт финальные цифры")
}

func TestGetSharesAreRelativeToVotersInMultipleChoice(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 0)
	poll := pollWithOptions(polldto.StatusActive, polldto.KindMultipleChoice, 2)

	// Сто человек, каждый отметил оба варианта: 100% за каждый, а не по 50%.
	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
	f.live.EXPECT().ReadCounts(gomock.Any(), poll.ID).Return(counters.Snapshot{
		Voters:  100,
		Options: map[uuid.UUID]int64{poll.Options[0].ID: 100, poll.Options[1].ID: 100},
		Found:   true,
	}, nil)

	got, err := f.service.Get(context.Background(), poll.ID)
	require.NoError(t, err)
	require.InDelta(t, 1.0, got.Options[0].Share, 1e-9)
	require.InDelta(t, 1.0, got.Options[1].Share, 1e-9)
}

func TestGetHandlesZeroVoters(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 0)
	poll := pollWithOptions(polldto.StatusActive, polldto.KindSingleChoice, 2)

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
	f.live.EXPECT().ReadCounts(gomock.Any(), poll.ID).Return(counters.Snapshot{Found: true}, nil)

	got, err := f.service.Get(context.Background(), poll.ID)
	require.NoError(t, err)
	require.Zero(t, got.Voters)
	require.Len(t, got.Options, 2)
	require.Zero(t, got.Options[0].Share, "деления на ноль быть не должно")
}

func TestGetCachesResults(t *testing.T) {
	t.Parallel()

	f := newFixture(t, time.Minute)
	poll := pollWithOptions(polldto.StatusActive, polldto.KindSingleChoice, 2)

	// Во время эфира результаты запрашивают все подряд: в Redis должен уходить
	// один запрос на интервал кэша, а не на каждого зрителя.
	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil).Times(1)
	f.live.EXPECT().ReadCounts(gomock.Any(), poll.ID).Return(counters.Snapshot{Voters: 1, Found: true}, nil).Times(1)

	for range 10 {
		_, err := f.service.Get(context.Background(), poll.ID)
		require.NoError(t, err)
	}
}

func TestGetReturnsNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 0)
	pollID := uuid.New()

	f.polls.EXPECT().Get(gomock.Any(), pollID).Return(polldto.Poll{}, polldto.ErrNotFound)

	_, err := f.service.Get(context.Background(), pollID)
	require.ErrorIs(t, err, polldto.ErrNotFound)
}

func TestTimelineIsSortedOldestFirst(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 0)
	poll := pollWithOptions(polldto.StatusClosed, polldto.KindSingleChoice, 1)
	now := time.Now()

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
	// Хранилище отдаёт свежие сверху, а для графика нужен обратный порядок.
	f.persisted.EXPECT().Timeline(gomock.Any(), poll.ID, gomock.Any()).Return([]dto.TimelinePoint{
		{CapturedAt: now, Voters: 30},
		{CapturedAt: now.Add(-time.Second), Voters: 20},
		{CapturedAt: now.Add(-2 * time.Second), Voters: 10},
	}, nil)

	points, err := f.service.Timeline(context.Background(), poll.ID, 10)
	require.NoError(t, err)
	require.Len(t, points, 3)
	require.Equal(t, int64(10), points[0].Voters)
	require.Equal(t, int64(30), points[2].Voters)
}
