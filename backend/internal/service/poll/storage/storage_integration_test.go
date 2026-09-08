//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
	"github.com/dneumoin/test_vote/backend/internal/service/poll/storage"
	boilerplate "github.com/dneumoin/test_vote/backend/internal/testing_boilerplate"
)

func newPoll(optionCount int) dto.Poll {
	poll := dto.Poll{
		ID:         uuid.New(),
		Title:      "Эфир",
		Question:   "Кто должен победить?",
		Kind:       dto.KindSingleChoice,
		Status:     dto.StatusDraft,
		MaxChoices: 1,
	}

	for i := range optionCount {
		poll.Options = append(poll.Options, dto.Option{
			ID: uuid.New(), Position: i, Text: "Вариант " + string(rune('А'+i)),
		})
	}

	return poll
}

func TestCreateAndGet(t *testing.T) {
	ctx := context.Background()
	store := storage.New(boilerplate.NewPostgres(t))

	created := newPoll(3)
	require.NoError(t, store.Create(ctx, created))

	got, err := store.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, created.Question, got.Question)
	require.Equal(t, dto.StatusDraft, got.Status)

	// Варианты должны приходить в том же порядке, в каком их задал админ:
	// на экране телевизора порядок кнопок менять нельзя.
	require.Len(t, got.Options, 3)
	for i, option := range got.Options {
		require.Equal(t, i, option.Position)
		require.Equal(t, created.Options[i].ID, option.ID)
		require.Equal(t, created.Options[i].Text, option.Text)
	}
}

func TestGetMissingPoll(t *testing.T) {
	store := storage.New(boilerplate.NewPostgres(t))

	_, err := store.GetByID(context.Background(), uuid.New())
	require.ErrorIs(t, err, dto.ErrNotFound)
}

func TestSetStatus(t *testing.T) {
	ctx := context.Background()
	store := storage.New(boilerplate.NewPostgres(t))

	created := newPoll(2)
	require.NoError(t, store.Create(ctx, created))
	require.NoError(t, store.SetStatus(ctx, created.ID, dto.StatusActive))

	got, err := store.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, dto.StatusActive, got.Status)

	require.ErrorIs(t, store.SetStatus(ctx, uuid.New(), dto.StatusActive), dto.ErrNotFound)
}

func TestListReturnsFreshFirst(t *testing.T) {
	ctx := context.Background()
	store := storage.New(boilerplate.NewPostgres(t))

	older := newPoll(2)
	require.NoError(t, store.Create(ctx, older))
	newer := newPoll(2)
	require.NoError(t, store.Create(ctx, newer))

	polls, err := store.List(ctx, 50, 0)
	require.NoError(t, err)
	require.NotEmpty(t, polls)

	positions := map[uuid.UUID]int{}
	for i, poll := range polls {
		positions[poll.ID] = i
	}

	require.Contains(t, positions, newer.ID)
	if olderPos, ok := positions[older.ID]; ok {
		require.Less(t, positions[newer.ID], olderPos)
	}
}

func TestListForSnapshotPicksActiveAndRecentlyClosed(t *testing.T) {
	ctx := context.Background()
	store := storage.New(boilerplate.NewPostgres(t))

	draft := newPoll(2)
	require.NoError(t, store.Create(ctx, draft))

	active := newPoll(2)
	require.NoError(t, store.Create(ctx, active))
	require.NoError(t, store.SetStatus(ctx, active.ID, dto.StatusActive))

	closed := newPoll(2)
	require.NoError(t, store.Create(ctx, closed))
	require.NoError(t, store.SetStatus(ctx, closed.ID, dto.StatusClosed))

	pollIDs, err := store.ListForSnapshot(ctx, time.Hour)
	require.NoError(t, err)

	watched := make(map[uuid.UUID]struct{}, len(pollIDs))
	for _, pollID := range pollIDs {
		watched[pollID] = struct{}{}
	}

	require.Contains(t, watched, active.ID)
	// Только что закрытый опрос нужен, чтобы финальные цифры доехали в базу.
	require.Contains(t, watched, closed.ID)
	// По черновику голосов быть не может, следить за ним незачем.
	require.NotContains(t, watched, draft.ID)
}
