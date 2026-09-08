//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/dneumoin/test_vote/backend/internal/counters"
	"github.com/dneumoin/test_vote/backend/internal/service/vote/storage"
	boilerplate "github.com/dneumoin/test_vote/backend/internal/testing_boilerplate"
)

func newStorage(t *testing.T, instanceID string) *storage.Storage {
	t.Helper()

	return storage.New(boilerplate.NewRedis(t), storage.Options{Shards: 16, InstanceID: instanceID})
}

func TestMarkVotedIsFirstOnlyOnce(t *testing.T) {
	ctx := context.Background()
	store := newStorage(t, "test-instance")
	key := "vote:d:test:" + uuid.NewString()

	first, err := store.MarkVoted(ctx, key, time.Minute)
	require.NoError(t, err)
	require.True(t, first)

	// Вся дедупликация держится на этом: второй раз тот же ключ должен вернуть false.
	second, err := store.MarkVoted(ctx, key, time.Minute)
	require.NoError(t, err)
	require.False(t, second)
}

func TestMarkVotedIsIndependentPerKey(t *testing.T) {
	ctx := context.Background()
	store := newStorage(t, "test-instance")

	for range 5 {
		first, err := store.MarkVoted(ctx, "vote:d:test:"+uuid.NewString(), time.Minute)
		require.NoError(t, err)
		require.True(t, first)
	}
}

func TestFlushAndReadCounts(t *testing.T) {
	ctx := context.Background()
	store := newStorage(t, "test-instance")

	pollID := uuid.New()
	first, second := uuid.New(), uuid.New()

	require.NoError(t, store.FlushDeltas(ctx, []counters.Delta{{
		PollID:  pollID,
		Voters:  3,
		Options: map[uuid.UUID]int64{first: 2, second: 1},
	}}))

	// Дельты именно прибавляются, а не перезаписывают счётчик.
	require.NoError(t, store.FlushDeltas(ctx, []counters.Delta{{
		PollID:  pollID,
		Voters:  1,
		Options: map[uuid.UUID]int64{second: 1},
	}}))

	snapshot, err := store.ReadCounts(ctx, pollID)
	require.NoError(t, err)
	require.True(t, snapshot.Found)
	require.Equal(t, int64(4), snapshot.Voters)
	require.Equal(t, int64(2), snapshot.Options[first])
	require.Equal(t, int64(2), snapshot.Options[second])
}

// TestReadCountsSumsAcrossShards — проверка ради того, зачем счётчики вообще
// шардируются: реплики пишут в разные ключи, а читатель должен видеть сумму.
func TestReadCountsSumsAcrossShards(t *testing.T) {
	ctx := context.Background()
	pollID := uuid.New()
	optionID := uuid.New()

	replicaA := newStorage(t, "replica-a")
	replicaB := newStorage(t, "replica-b")

	require.NoError(t, replicaA.FlushDeltas(ctx, []counters.Delta{{
		PollID: pollID, Voters: 5, Options: map[uuid.UUID]int64{optionID: 5},
	}}))
	require.NoError(t, replicaB.FlushDeltas(ctx, []counters.Delta{{
		PollID: pollID, Voters: 7, Options: map[uuid.UUID]int64{optionID: 7},
	}}))

	snapshot, err := replicaA.ReadCounts(ctx, pollID)
	require.NoError(t, err)
	require.Equal(t, int64(12), snapshot.Voters)
	require.Equal(t, int64(12), snapshot.Options[optionID])
}

func TestReadCountsForUnknownPoll(t *testing.T) {
	store := newStorage(t, "test-instance")

	snapshot, err := store.ReadCounts(context.Background(), uuid.New())
	require.NoError(t, err)
	require.False(t, snapshot.Found, "по опросу без голосов счётчиков быть не должно")
	require.Zero(t, snapshot.Voters)
}

func TestFlushEmptyDeltasIsNoop(t *testing.T) {
	store := newStorage(t, "test-instance")

	require.NoError(t, store.FlushDeltas(context.Background(), nil))
}
