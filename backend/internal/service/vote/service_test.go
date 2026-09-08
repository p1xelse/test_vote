package vote_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/dneumoin/test_vote/backend/internal/counters"
	"github.com/dneumoin/test_vote/backend/internal/dedup"
	"github.com/dneumoin/test_vote/backend/internal/observability"
	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
	"github.com/dneumoin/test_vote/backend/internal/service/vote"
	"github.com/dneumoin/test_vote/backend/internal/service/vote/dto"
	"github.com/dneumoin/test_vote/backend/internal/service/vote/mocks"
)

type fixture struct {
	service    *vote.Service
	polls      *mocks.MockPollProvider
	marker     *mocks.MockMarker
	aggregator *counters.Aggregator
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	ctrl := gomock.NewController(t)
	polls := mocks.NewMockPollProvider(ctrl)
	marker := mocks.NewMockMarker(ctrl)
	aggregator := counters.NewAggregator(4)

	return &fixture{
		service:    vote.New(polls, marker, aggregator, observability.NewMetrics(), time.Hour),
		polls:      polls,
		marker:     marker,
		aggregator: aggregator,
	}
}

func activePoll(kind polldto.Kind, maxChoices, optionCount int) polldto.Poll {
	poll := polldto.Poll{
		ID:         uuid.New(),
		Kind:       kind,
		Status:     polldto.StatusActive,
		MaxChoices: maxChoices,
	}

	for i := range optionCount {
		poll.Options = append(poll.Options, polldto.Option{ID: uuid.New(), Position: i})
	}

	return poll
}

func identity() dedup.Identity { return dedup.CookieIdentity(uuid.New()) }

func TestCastAcceptsFirstVote(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	poll := activePoll(polldto.KindSingleChoice, 1, 3)

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
	f.marker.EXPECT().MarkVoted(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)

	result, err := f.service.Cast(context.Background(), dto.CastParams{
		PollID:    poll.ID,
		OptionIDs: []uuid.UUID{poll.Options[1].ID},
		Identity:  identity(),
	})

	require.NoError(t, err)
	require.Equal(t, dto.OutcomeAccepted, result.Outcome)

	deltas := f.aggregator.Drain()
	require.Len(t, deltas, 1)
	require.Equal(t, int64(1), deltas[0].Voters)
	require.Equal(t, int64(1), deltas[0].Options[poll.Options[1].ID])
}

func TestCastRejectsRepeatVote(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	poll := activePoll(polldto.KindSingleChoice, 1, 2)

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
	// Отметка уже стояла — значит этот зритель голосовал раньше.
	f.marker.EXPECT().MarkVoted(gomock.Any(), gomock.Any(), gomock.Any()).Return(false, nil)

	result, err := f.service.Cast(context.Background(), dto.CastParams{
		PollID:    poll.ID,
		OptionIDs: []uuid.UUID{poll.Options[0].ID},
		Identity:  identity(),
	})

	require.NoError(t, err)
	require.Equal(t, dto.OutcomeDuplicate, result.Outcome)
	require.Empty(t, f.aggregator.Drain(), "повторный голос не должен попадать в счётчики")
}

func TestCastRejectsClosedPoll(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	poll := activePoll(polldto.KindSingleChoice, 1, 2)
	poll.Status = polldto.StatusClosed

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)

	_, err := f.service.Cast(context.Background(), dto.CastParams{
		PollID:    poll.ID,
		OptionIDs: []uuid.UUID{poll.Options[0].ID},
		Identity:  identity(),
	})

	require.ErrorIs(t, err, dto.ErrPollNotAccepting)
}

func TestCastRejectsForeignOption(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	poll := activePoll(polldto.KindSingleChoice, 1, 2)

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)

	_, err := f.service.Cast(context.Background(), dto.CastParams{
		PollID:    poll.ID,
		OptionIDs: []uuid.UUID{uuid.New()},
		Identity:  identity(),
	})

	require.ErrorIs(t, err, dto.ErrInvalidOption)
}

func TestCastChoiceCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		kind       polldto.Kind
		maxChoices int
		choices    int
		repeat     bool
		wantErr    bool
	}{
		{name: "один выбор в single_choice", kind: polldto.KindSingleChoice, maxChoices: 1, choices: 1},
		{name: "два выбора в single_choice", kind: polldto.KindSingleChoice, maxChoices: 1, choices: 2, wantErr: true},
		{name: "два выбора при лимите два", kind: polldto.KindMultipleChoice, maxChoices: 2, choices: 2},
		{name: "три выбора при лимите два", kind: polldto.KindMultipleChoice, maxChoices: 2, choices: 3, wantErr: true},
		{name: "ноль выборов", kind: polldto.KindSingleChoice, maxChoices: 1, choices: 0, wantErr: true},
		{name: "один вариант дважды", kind: polldto.KindMultipleChoice, maxChoices: 3, choices: 1, repeat: true, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			poll := activePoll(tc.kind, tc.maxChoices, 4)

			f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
			if !tc.wantErr {
				f.marker.EXPECT().MarkVoted(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)
			}

			optionIDs := make([]uuid.UUID, 0, tc.choices)
			for i := range tc.choices {
				optionIDs = append(optionIDs, poll.Options[i].ID)
			}
			if tc.repeat {
				optionIDs = append(optionIDs, optionIDs[0])
			}

			_, err := f.service.Cast(context.Background(), dto.CastParams{
				PollID:    poll.ID,
				OptionIDs: optionIDs,
				Identity:  identity(),
			})

			if tc.wantErr {
				require.ErrorIs(t, err, dto.ErrChoiceCount)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestCastDoesNotCountVoteWhenDedupFails(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	poll := activePoll(polldto.KindSingleChoice, 1, 2)
	boom := errors.New("redis is down")

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
	f.marker.EXPECT().MarkVoted(gomock.Any(), gomock.Any(), gomock.Any()).Return(false, boom)

	_, err := f.service.Cast(context.Background(), dto.CastParams{
		PollID:    poll.ID,
		OptionIDs: []uuid.UUID{poll.Options[0].ID},
		Identity:  identity(),
	})

	require.ErrorIs(t, err, boom)
	require.Empty(t, f.aggregator.Drain(),
		"без подтверждённой дедупликации голос не должен попадать в счётчики")
}

func TestCastShortensDedupTTLForPollWithDeadline(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	poll := activePoll(polldto.KindSingleChoice, 1, 2)
	closesAt := time.Now().Add(time.Minute)
	poll.ClosesAt = &closesAt

	f.polls.EXPECT().Get(gomock.Any(), poll.ID).Return(poll, nil)
	f.marker.EXPECT().
		MarkVoted(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, ttl time.Duration) (bool, error) {
			// Держать сутки ключи минутного опроса — впустую занятая память Redis.
			require.Less(t, ttl, time.Hour)
			require.Greater(t, ttl, time.Minute)

			return true, nil
		})

	_, err := f.service.Cast(context.Background(), dto.CastParams{
		PollID:    poll.ID,
		OptionIDs: []uuid.UUID{poll.Options[0].ID},
		Identity:  identity(),
	})

	require.NoError(t, err)
}
