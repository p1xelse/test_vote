package poll_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/dneumoin/test_vote/backend/internal/service/poll"
	"github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
	"github.com/dneumoin/test_vote/backend/internal/service/poll/mocks"
)

func TestCreateValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		params  dto.CreateParams
		wantErr bool
	}{
		{
			name:   "обычный опрос с одним выбором",
			params: dto.CreateParams{Title: "Эфир", Question: "Кто?", Kind: dto.KindSingleChoice, Options: []string{"А", "Б"}},
		},
		{
			name:   "a/b ровно с двумя вариантами",
			params: dto.CreateParams{Title: "Эфир", Question: "Кто?", Kind: dto.KindAB, Options: []string{"А", "Б"}},
		},
		{
			name: "множественный выбор с лимитом",
			params: dto.CreateParams{
				Title: "Эфир", Question: "Кто?", Kind: dto.KindMultipleChoice,
				MaxChoices: 2, Options: []string{"А", "Б", "В"},
			},
		},
		{
			name:    "пустой вопрос",
			params:  dto.CreateParams{Title: "Эфир", Question: "  ", Kind: dto.KindSingleChoice, Options: []string{"А", "Б"}},
			wantErr: true,
		},
		{
			name:    "неизвестный тип",
			params:  dto.CreateParams{Title: "Эфир", Question: "Кто?", Kind: "quiz", Options: []string{"А", "Б"}},
			wantErr: true,
		},
		{
			name:    "один вариант",
			params:  dto.CreateParams{Title: "Эфир", Question: "Кто?", Kind: dto.KindSingleChoice, Options: []string{"А"}},
			wantErr: true,
		},
		{
			name:    "a/b с тремя вариантами",
			params:  dto.CreateParams{Title: "Эфир", Question: "Кто?", Kind: dto.KindAB, Options: []string{"А", "Б", "В"}},
			wantErr: true,
		},
		{
			name:    "повторяющиеся варианты",
			params:  dto.CreateParams{Title: "Эфир", Question: "Кто?", Kind: dto.KindSingleChoice, Options: []string{"А", "А"}},
			wantErr: true,
		},
		{
			name: "max_choices больше числа вариантов",
			params: dto.CreateParams{
				Title: "Эфир", Question: "Кто?", Kind: dto.KindMultipleChoice,
				MaxChoices: 5, Options: []string{"А", "Б"},
			},
			wantErr: true,
		},
		{
			name: "max_choices > 1 без множественного выбора",
			params: dto.CreateParams{
				Title: "Эфир", Question: "Кто?", Kind: dto.KindSingleChoice,
				MaxChoices: 2, Options: []string{"А", "Б"},
			},
			wantErr: true,
		},
		{
			name: "окно голосования наизнанку",
			params: dto.CreateParams{
				Title: "Эфир", Question: "Кто?", Kind: dto.KindSingleChoice, Options: []string{"А", "Б"},
				OpensAt: ptr(time.Now().Add(time.Hour)), ClosesAt: ptr(time.Now()),
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			storage := mocks.NewMockStorage(ctrl)

			if !tc.wantErr {
				storage.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			}

			created, err := poll.New(storage, time.Second).Create(context.Background(), tc.params)

			if tc.wantErr {
				require.ErrorIs(t, err, dto.ErrValidation)

				return
			}

			require.NoError(t, err)
			require.Equal(t, dto.StatusDraft, created.Status, "новый опрос не должен сразу принимать голоса")
			require.Len(t, created.Options, len(tc.params.Options))
			require.Equal(t, 0, created.Options[0].Position)
		})
	}
}

func TestGetCachesPoll(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	storage := mocks.NewMockStorage(ctrl)
	pollID := uuid.New()

	// Ровно один поход в базу на несколько чтений — иначе на пике опроса
	// PostgreSQL получил бы запрос на каждый голос.
	storage.EXPECT().GetByID(gomock.Any(), pollID).Return(dto.Poll{ID: pollID}, nil).Times(1)

	service := poll.New(storage, time.Minute)
	for range 5 {
		got, err := service.Get(context.Background(), pollID)
		require.NoError(t, err)
		require.Equal(t, pollID, got.ID)
	}
}

func TestGetCachesNotFound(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	storage := mocks.NewMockStorage(ctrl)
	pollID := uuid.New()

	// Перебор случайных id не должен превращаться в поток запросов к базе.
	storage.EXPECT().GetByID(gomock.Any(), pollID).Return(dto.Poll{}, dto.ErrNotFound).Times(1)

	service := poll.New(storage, time.Minute)
	for range 3 {
		_, err := service.Get(context.Background(), pollID)
		require.ErrorIs(t, err, dto.ErrNotFound)
	}
}

func TestGetDoesNotCacheInfrastructureErrors(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	storage := mocks.NewMockStorage(ctrl)
	pollID := uuid.New()
	boom := errors.New("connection refused")

	// Недоступность базы — временная проблема, кэшировать её нельзя.
	storage.EXPECT().GetByID(gomock.Any(), pollID).Return(dto.Poll{}, boom).Times(2)

	service := poll.New(storage, time.Minute)
	for range 2 {
		_, err := service.Get(context.Background(), pollID)
		require.ErrorIs(t, err, boom)
	}
}

func TestSetStatusTransitions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		from    dto.Status
		to      dto.Status
		allowed bool
	}{
		{name: "draft -> active", from: dto.StatusDraft, to: dto.StatusActive, allowed: true},
		{name: "draft -> closed", from: dto.StatusDraft, to: dto.StatusClosed, allowed: true},
		{name: "active -> closed", from: dto.StatusActive, to: dto.StatusClosed, allowed: true},
		{name: "active -> draft", from: dto.StatusActive, to: dto.StatusDraft},
		{name: "closed -> active", from: dto.StatusClosed, to: dto.StatusActive},
		{name: "active -> active", from: dto.StatusActive, to: dto.StatusActive},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			storage := mocks.NewMockStorage(ctrl)
			pollID := uuid.New()

			storage.EXPECT().GetByID(gomock.Any(), pollID).Return(dto.Poll{ID: pollID, Status: tc.from}, nil)
			if tc.allowed {
				storage.EXPECT().SetStatus(gomock.Any(), pollID, tc.to).Return(nil)
			}

			updated, err := poll.New(storage, time.Second).SetStatus(context.Background(), pollID, tc.to)

			if !tc.allowed {
				require.ErrorIs(t, err, dto.ErrInvalidStatus)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.to, updated.Status)
		})
	}
}

func TestSetStatusInvalidatesCache(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	storage := mocks.NewMockStorage(ctrl)
	pollID := uuid.New()

	gomock.InOrder(
		storage.EXPECT().GetByID(gomock.Any(), pollID).Return(dto.Poll{ID: pollID, Status: dto.StatusActive}, nil),
		storage.EXPECT().GetByID(gomock.Any(), pollID).Return(dto.Poll{ID: pollID, Status: dto.StatusActive}, nil),
		storage.EXPECT().SetStatus(gomock.Any(), pollID, dto.StatusClosed).Return(nil),
		storage.EXPECT().GetByID(gomock.Any(), pollID).Return(dto.Poll{ID: pollID, Status: dto.StatusClosed}, nil),
	)

	service := poll.New(storage, time.Minute)

	_, err := service.Get(context.Background(), pollID)
	require.NoError(t, err)

	_, err = service.SetStatus(context.Background(), pollID, dto.StatusClosed)
	require.NoError(t, err)

	// Закрытие опроса должно быть видно немедленно, не дожидаясь протухания кэша.
	got, err := service.Get(context.Background(), pollID)
	require.NoError(t, err)
	require.Equal(t, dto.StatusClosed, got.Status)
}

func TestAcceptsVotesAt(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 8, 21, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		poll dto.Poll
		want bool
	}{
		{name: "активный без окна", poll: dto.Poll{Status: dto.StatusActive}, want: true},
		{name: "черновик", poll: dto.Poll{Status: dto.StatusDraft}},
		{name: "закрытый", poll: dto.Poll{Status: dto.StatusClosed}},
		{
			name: "активный, но окно ещё не открылось",
			poll: dto.Poll{Status: dto.StatusActive, OpensAt: ptr(now.Add(time.Minute))},
		},
		{
			name: "активный, но окно уже закрылось",
			poll: dto.Poll{Status: dto.StatusActive, ClosesAt: ptr(now.Add(-time.Second))},
		},
		{
			name: "активный внутри окна",
			poll: dto.Poll{
				Status:  dto.StatusActive,
				OpensAt: ptr(now.Add(-time.Minute)), ClosesAt: ptr(now.Add(time.Minute)),
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, tc.poll.AcceptsVotesAt(now))
		})
	}
}

func ptr[T any](value T) *T { return &value }
