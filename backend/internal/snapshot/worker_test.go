package snapshot_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/dneumoin/test_vote/backend/internal/counters"
	"github.com/dneumoin/test_vote/backend/internal/observability"
	"github.com/dneumoin/test_vote/backend/internal/snapshot"
	"github.com/dneumoin/test_vote/backend/internal/snapshot/mocks"
)

// tickInterval укорочен, чтобы тест не ждал: в проде это пара секунд.
const tickInterval = 10 * time.Millisecond

type fixture struct {
	worker  *snapshot.Worker
	polls   *mocks.MockPollLister
	live    *mocks.MockLiveCounters
	storage *mocks.MockResultsStorage
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	ctrl := gomock.NewController(t)
	f := &fixture{
		polls:   mocks.NewMockPollLister(ctrl),
		live:    mocks.NewMockLiveCounters(ctrl),
		storage: mocks.NewMockResultsStorage(ctrl),
	}
	f.worker = snapshot.New(
		f.polls, f.live, f.storage, tickInterval,
		slog.New(slog.DiscardHandler), observability.NewMetrics(),
	)

	return f
}

// runOnce гоняет воркер ровно до первого срабатывания тикера плюс финальный
// проход по отмене контекста.
func runOnce(t *testing.T, f *fixture) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		f.worker.Run(ctx)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("воркер не остановился по отмене контекста")
	}
}

func TestWorkerSavesCounters(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	pollID := uuid.New()
	optionID := uuid.New()

	f.polls.EXPECT().ListForSnapshot(gomock.Any(), gomock.Any()).Return([]uuid.UUID{pollID}, nil).AnyTimes()
	f.live.EXPECT().ReadCounts(gomock.Any(), pollID).Return(counters.Snapshot{
		Voters:  5,
		Options: map[uuid.UUID]int64{optionID: 5},
		Found:   true,
	}, nil).AnyTimes()

	// Цифры не менялись, поэтому запись должна быть ровно одна, сколько бы раз
	// ни сработал тикер.
	f.storage.EXPECT().Save(gomock.Any(), pollID, int64(5), gomock.Any()).Return(nil).Times(1)

	runOnce(t, f)
}

func TestWorkerSkipsPollsWithoutCounters(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	pollID := uuid.New()

	f.polls.EXPECT().ListForSnapshot(gomock.Any(), gomock.Any()).Return([]uuid.UUID{pollID}, nil).AnyTimes()
	// По опросу ещё не голосовали — писать в базу нули незачем.
	f.live.EXPECT().ReadCounts(gomock.Any(), pollID).Return(counters.Snapshot{Found: false}, nil).AnyTimes()

	runOnce(t, f)
}

func TestWorkerSurvivesStorageErrors(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	pollID := uuid.New()

	f.polls.EXPECT().ListForSnapshot(gomock.Any(), gomock.Any()).Return([]uuid.UUID{pollID}, nil).AnyTimes()
	f.live.EXPECT().ReadCounts(gomock.Any(), pollID).
		Return(counters.Snapshot{Voters: 1, Options: map[uuid.UUID]int64{}, Found: true}, nil).AnyTimes()
	// Неудачная запись не должна запоминаться как успешная: следующий проход
	// обязан попробовать снова.
	f.storage.EXPECT().Save(gomock.Any(), pollID, int64(1), gomock.Any()).
		Return(errors.New("write failed")).MinTimes(2)

	runOnce(t, f)
}

func TestWorkerSurvivesListErrors(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	f.polls.EXPECT().ListForSnapshot(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("database is down")).AnyTimes()

	require.NotPanics(t, func() { runOnce(t, f) })
}
