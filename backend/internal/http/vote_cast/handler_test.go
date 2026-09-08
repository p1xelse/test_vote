package votecast_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/dneumoin/test_vote/backend/internal/dedup"
	votecast "github.com/dneumoin/test_vote/backend/internal/http/vote_cast"
	"github.com/dneumoin/test_vote/backend/internal/http/vote_cast/mocks"
	votedto "github.com/dneumoin/test_vote/backend/internal/service/vote/dto"
)

func call(
	t *testing.T,
	handler *votecast.Handler,
	pollID, body string,
) (status int, responseBody string) {
	t.Helper()

	e := echo.New()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()

	c := e.NewContext(request, recorder)
	c.SetPath("/api/v1/polls/:poll_id/vote")
	c.SetParamNames("poll_id")
	c.SetParamValues(pollID)

	if err := handler.Handle(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}

	return recorder.Code, recorder.Body.String()
}

func TestHandleAcceptsVote(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	votes := mocks.NewMockVoter(ctrl)
	voters := mocks.NewMockIdentifier(ctrl)

	pollID, optionID := uuid.New(), uuid.New()
	identity := dedup.CookieIdentity(uuid.New())

	voters.EXPECT().Identify(gomock.Any()).Return(identity)
	votes.EXPECT().
		Cast(gomock.Any(), votedto.CastParams{
			PollID:    pollID,
			OptionIDs: []uuid.UUID{optionID},
			Identity:  identity,
		}).
		Return(votedto.CastResult{Outcome: votedto.OutcomeAccepted}, nil)

	code, body := call(t, votecast.New(votes, voters), pollID.String(),
		`{"option_ids":["`+optionID.String()+`"]}`)

	require.Equal(t, http.StatusAccepted, code)
	require.Contains(t, body, `"outcome":"accepted"`)
}

func TestHandleReportsDuplicate(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	votes := mocks.NewMockVoter(ctrl)
	voters := mocks.NewMockIdentifier(ctrl)

	pollID, optionID := uuid.New(), uuid.New()

	voters.EXPECT().Identify(gomock.Any()).Return(dedup.CookieIdentity(uuid.New()))
	votes.EXPECT().Cast(gomock.Any(), gomock.Any()).
		Return(votedto.CastResult{Outcome: votedto.OutcomeDuplicate}, nil)

	code, body := call(t, votecast.New(votes, voters), pollID.String(),
		`{"option_ids":["`+optionID.String()+`"]}`)

	require.Equal(t, http.StatusConflict, code)
	require.Contains(t, body, `"code":"already_voted"`)
}

func TestHandleMapsDomainErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{
			name:     "закрытый опрос",
			err:      votedto.ErrPollNotAccepting,
			wantCode: http.StatusGone,
			wantBody: "poll_not_accepting",
		},
		{
			name:     "чужой вариант ответа",
			err:      votedto.ErrInvalidOption,
			wantCode: http.StatusBadRequest,
			wantBody: "validation_error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			votes := mocks.NewMockVoter(ctrl)
			voters := mocks.NewMockIdentifier(ctrl)

			voters.EXPECT().Identify(gomock.Any()).Return(dedup.CookieIdentity(uuid.New()))
			votes.EXPECT().Cast(gomock.Any(), gomock.Any()).Return(votedto.CastResult{}, tc.err)

			code, body := call(t, votecast.New(votes, voters), uuid.NewString(),
				`{"option_ids":["`+uuid.NewString()+`"]}`)

			require.Equal(t, tc.wantCode, code)
			require.Contains(t, body, tc.wantBody)
		})
	}
}

// TestHandleRejectsMalformedInput проверяет, что до сервиса не доходит ничего
// заведомо мусорного: сервис на горячем пути не должен тратить на это время.
func TestHandleRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		pollID string
		body   string
	}{
		{name: "не uuid в пути", pollID: "not-a-uuid", body: `{"option_ids":["` + uuid.NewString() + `"]}`},
		{name: "сломанный json", pollID: uuid.NewString(), body: `{`},
		{name: "пустой список вариантов", pollID: uuid.NewString(), body: `{"option_ids":[]}`},
		{name: "не uuid в вариантах", pollID: uuid.NewString(), body: `{"option_ids":["nope"]}`},
		{name: "слишком много вариантов", pollID: uuid.NewString(), body: manyOptions(33)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			// Ни Cast, ни Identify вызываться не должны: ожиданий не задаём,
			// и gomock упадёт на любом обращении.
			handler := votecast.New(mocks.NewMockVoter(ctrl), mocks.NewMockIdentifier(ctrl))

			code, body := call(t, handler, tc.pollID, tc.body)

			require.Equal(t, http.StatusBadRequest, code)
			require.Contains(t, body, "validation_error")
		})
	}
}

func manyOptions(count int) string {
	ids := make([]string, 0, count)
	for range count {
		ids = append(ids, `"`+uuid.NewString()+`"`)
	}

	return `{"option_ids":[` + strings.Join(ids, ",") + `]}`
}
