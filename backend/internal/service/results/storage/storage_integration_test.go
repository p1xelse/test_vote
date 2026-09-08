//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
	pollstorage "github.com/dneumoin/test_vote/backend/internal/service/poll/storage"
	"github.com/dneumoin/test_vote/backend/internal/service/results/storage"
	boilerplate "github.com/dneumoin/test_vote/backend/internal/testing_boilerplate"
)

func TestSaveAndLoad(t *testing.T) {
	ctx := context.Background()
	pg := boilerplate.NewPostgres(t)

	poll := polldto.Poll{
		ID: uuid.New(), Title: "Эфир", Question: "Кто?",
		Kind: polldto.KindSingleChoice, Status: polldto.StatusActive, MaxChoices: 1,
		Options: []polldto.Option{
			{ID: uuid.New(), Position: 0, Text: "А"},
			{ID: uuid.New(), Position: 1, Text: "Б"},
		},
	}
	require.NoError(t, pollstorage.New(pg).Create(ctx, poll))

	store := storage.New(pg)
	counts := map[uuid.UUID]int64{poll.Options[0].ID: 7, poll.Options[1].ID: 3}
	require.NoError(t, store.Save(ctx, poll.ID, 10, counts))

	voters, loaded, err := store.Load(ctx, poll.ID)
	require.NoError(t, err)
	require.Equal(t, int64(10), voters)
	require.Equal(t, counts, loaded)

	// Повторное сохранение перезаписывает итог, а не удваивает его.
	updated := map[uuid.UUID]int64{poll.Options[0].ID: 9, poll.Options[1].ID: 4}
	require.NoError(t, store.Save(ctx, poll.ID, 13, updated))

	voters, loaded, err = store.Load(ctx, poll.ID)
	require.NoError(t, err)
	require.Equal(t, int64(13), voters)
	require.Equal(t, updated, loaded)
}

func TestLoadMissingPollIsEmpty(t *testing.T) {
	store := storage.New(boilerplate.NewPostgres(t))

	voters, counts, err := store.Load(context.Background(), uuid.New())
	require.NoError(t, err, "отсутствие снапшотов — не ошибка")
	require.Zero(t, voters)
	require.Empty(t, counts)
}

func TestTimelineKeepsEverySnapshot(t *testing.T) {
	ctx := context.Background()
	pg := boilerplate.NewPostgres(t)

	poll := polldto.Poll{
		ID: uuid.New(), Title: "Эфир", Question: "Кто?",
		Kind: polldto.KindAB, Status: polldto.StatusActive, MaxChoices: 1,
		Options: []polldto.Option{
			{ID: uuid.New(), Position: 0, Text: "А"},
			{ID: uuid.New(), Position: 1, Text: "Б"},
		},
	}
	require.NoError(t, pollstorage.New(pg).Create(ctx, poll))

	store := storage.New(pg)
	for _, voters := range []int64{10, 20, 30} {
		require.NoError(t, store.Save(ctx, poll.ID, voters,
			map[uuid.UUID]int64{poll.Options[0].ID: voters}))
	}

	points, err := store.Timeline(ctx, poll.ID, 10)
	require.NoError(t, err)
	require.Len(t, points, 3)

	// Хранилище отдаёт свежие сверху; сортировкой для графика занимается сервис.
	require.Equal(t, int64(30), points[0].Voters)
	require.Equal(t, int64(30), points[0].Counts[poll.Options[0].ID])
}
