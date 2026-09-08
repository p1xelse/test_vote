package counters_test

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/dneumoin/test_vote/backend/internal/counters"
)

func TestAggregatorCountsVotes(t *testing.T) {
	t.Parallel()

	pollID := uuid.New()
	first, second := uuid.New(), uuid.New()

	aggregator := counters.NewAggregator(8)
	aggregator.AddVote(pollID, []uuid.UUID{first})
	aggregator.AddVote(pollID, []uuid.UUID{first})
	aggregator.AddVote(pollID, []uuid.UUID{second})

	deltas := aggregator.Drain()
	require.Len(t, deltas, 1)
	require.Equal(t, pollID, deltas[0].PollID)
	require.Equal(t, int64(3), deltas[0].Voters)
	require.Equal(t, int64(2), deltas[0].Options[first])
	require.Equal(t, int64(1), deltas[0].Options[second])
}

func TestAggregatorCountsVoterOncePerMultipleChoice(t *testing.T) {
	t.Parallel()

	pollID := uuid.New()
	first, second := uuid.New(), uuid.New()

	aggregator := counters.NewAggregator(4)
	aggregator.AddVote(pollID, []uuid.UUID{first, second})

	deltas := aggregator.Drain()
	require.Len(t, deltas, 1)
	// Один человек — один голосующий, но две отметки: именно из-за этого
	// проценты считаются от Voters, а не от суммы по вариантам.
	require.Equal(t, int64(1), deltas[0].Voters)
	require.Equal(t, int64(2), deltas[0].Total())
}

func TestAggregatorDrainResetsCounters(t *testing.T) {
	t.Parallel()

	pollID := uuid.New()
	optionID := uuid.New()

	aggregator := counters.NewAggregator(4)
	aggregator.AddVote(pollID, []uuid.UUID{optionID})

	require.Len(t, aggregator.Drain(), 1)
	require.Empty(t, aggregator.Drain(), "второй Drain подряд не должен ничего возвращать")
}

func TestAggregatorRestoreReturnsDeltas(t *testing.T) {
	t.Parallel()

	pollID := uuid.New()
	optionID := uuid.New()

	aggregator := counters.NewAggregator(4)
	aggregator.AddVote(pollID, []uuid.UUID{optionID})

	// Имитируем неудачный сброс: дельты вернулись в агрегатор и должны дожить
	// до следующей попытки.
	aggregator.Restore(aggregator.Drain())

	deltas := aggregator.Drain()
	require.Len(t, deltas, 1)
	require.Equal(t, int64(1), deltas[0].Voters)
	require.Equal(t, int64(1), deltas[0].Options[optionID])
}

// TestAggregatorConcurrentAddDoesNotLoseVotes — главный тест пакета: под гонкой
// не должен потеряться ни один голос, включая те, что попали в Drain по пути.
func TestAggregatorConcurrentAddDoesNotLoseVotes(t *testing.T) {
	t.Parallel()

	const (
		writers        = 16
		votesPerWriter = 2_000
	)

	pollID := uuid.New()
	optionID := uuid.New()
	aggregator := counters.NewAggregator(64)

	var drained int64
	var drainMu sync.Mutex

	done := make(chan struct{})
	var drainer sync.WaitGroup

	drainer.Add(1)
	go func() {
		defer drainer.Done()

		for {
			select {
			case <-done:
				return
			default:
			}

			for _, delta := range aggregator.Drain() {
				drainMu.Lock()
				drained += delta.Options[optionID]
				drainMu.Unlock()
			}
		}
	}()

	var writersGroup sync.WaitGroup
	writersGroup.Add(writers)
	for range writers {
		go func() {
			defer writersGroup.Done()

			for range votesPerWriter {
				aggregator.AddVote(pollID, []uuid.UUID{optionID})
			}
		}()
	}

	writersGroup.Wait()
	close(done)
	drainer.Wait()

	for _, delta := range aggregator.Drain() {
		drained += delta.Options[optionID]
	}

	require.Equal(t, int64(writers*votesPerWriter), drained)
}

func BenchmarkAggregatorAddVote(b *testing.B) {
	pollID := uuid.New()
	optionID := uuid.New()
	options := []uuid.UUID{optionID}
	aggregator := counters.NewAggregator(64)

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			aggregator.AddVote(pollID, options)
		}
	})
}

// BenchmarkAggregatorAddVoteSingleStripe показывает, сколько стоит отказ от
// раскладки счётчика по кэш-линиям.
func BenchmarkAggregatorAddVoteSingleStripe(b *testing.B) {
	pollID := uuid.New()
	optionID := uuid.New()
	options := []uuid.UUID{optionID}
	aggregator := counters.NewAggregator(1)

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			aggregator.AddVote(pollID, options)
		}
	})
}
