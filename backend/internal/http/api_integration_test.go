//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/dneumoin/test_vote/backend/internal/counters"
	"github.com/dneumoin/test_vote/backend/internal/dedup"
	httpapi "github.com/dneumoin/test_vote/backend/internal/http"
	"github.com/dneumoin/test_vote/backend/internal/http/middleware"
	"github.com/dneumoin/test_vote/backend/internal/http/voter"
	"github.com/dneumoin/test_vote/backend/internal/observability"
	"github.com/dneumoin/test_vote/backend/internal/service/poll"
	pollstorage "github.com/dneumoin/test_vote/backend/internal/service/poll/storage"
	"github.com/dneumoin/test_vote/backend/internal/service/results"
	resultsstorage "github.com/dneumoin/test_vote/backend/internal/service/results/storage"
	"github.com/dneumoin/test_vote/backend/internal/service/vote"
	votestorage "github.com/dneumoin/test_vote/backend/internal/service/vote/storage"
	boilerplate "github.com/dneumoin/test_vote/backend/internal/testing_boilerplate"
)

const (
	adminToken = "integration-admin-token"
	// flushInterval укорочен, чтобы тест не ждал: в проде это 200 мс.
	flushInterval = 20 * time.Millisecond
)

type env struct {
	t      *testing.T
	server *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()

	pg := boilerplate.NewPostgres(t)
	redis := boilerplate.NewRedis(t)
	metrics := observability.NewMetrics()
	logger := slog.New(slog.DiscardHandler)

	// Кэши выключены (TTL = 0), иначе тест ловил бы устаревшие цифры.
	pollService := poll.New(pollstorage.New(pg), 0)
	voteStore := votestorage.New(redis, votestorage.Options{Shards: 8, InstanceID: t.Name()})
	aggregator := counters.NewAggregator(8)
	voteService := vote.New(pollService, voteStore, aggregator, metrics, time.Hour)
	resultsService := results.New(pollService, voteStore, resultsstorage.New(pg), 0)

	ctx, cancel := context.WithCancel(context.Background())
	flusher := counters.NewFlusher(aggregator, voteStore, flushInterval, logger, metrics)
	go flusher.Run(ctx)
	t.Cleanup(cancel)

	handler := httpapi.New(httpapi.Deps{
		Polls:      pollService,
		PollsAdmin: pollService,
		Votes:      voteService,
		Results:    resultsService,
		Voters: voter.New(
			dedup.NewSigner("integration-secret-at-least-16"), "vid", time.Hour, false,
		),
		// Лимит поднят: весь тест приходит с одного адреса.
		RateLimiter: middleware.NewRateLimiter(1e6, 1e6),
		Metrics:     metrics,
		Logger:      logger,
		AdminToken:  adminToken,
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &env{t: t, server: server}
}

// newViewer возвращает клиента с собственной банкой кук — то есть отдельного зрителя.
func (e *env) newViewer() *http.Client {
	e.t.Helper()

	jar, err := cookiejar.New(nil)
	require.NoError(e.t, err)

	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func (e *env) do(
	client *http.Client,
	method, path string,
	body any,
	token string,
) (status int, decoded map[string]any) {
	e.t.Helper()

	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(e.t, err)
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}

	request, err := http.NewRequestWithContext(e.t.Context(), method, e.server.URL+path, reader)
	require.NoError(e.t, err)
	request.Header.Set("Content-Type", "application/json")

	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := client.Do(request)
	require.NoError(e.t, err)
	defer func() { _ = response.Body.Close() }()

	decoded = map[string]any{}
	_ = json.NewDecoder(response.Body).Decode(&decoded)

	return response.StatusCode, decoded
}

func (e *env) admin(method, path string, body any) (status int, decoded map[string]any) {
	e.t.Helper()

	return e.do(e.newViewer(), method, path, body, adminToken)
}

// createActivePoll создаёт опрос и сразу открывает голосование.
func (e *env) createActivePoll(
	kind string,
	options []string,
	maxChoices int,
) (pollID string, optionIDs []string) {
	e.t.Helper()

	status, body := e.admin(http.MethodPost, "/api/v1/admin/polls", map[string]any{
		"title":       "Интеграционный эфир",
		"question":    "Кто должен победить?",
		"kind":        kind,
		"max_choices": maxChoices,
		"options":     options,
	})
	require.Equal(e.t, http.StatusCreated, status, body)

	pollID, _ = body["id"].(string)
	rawOptions, _ := body["options"].([]any)

	optionIDs = make([]string, 0, len(rawOptions))
	for _, raw := range rawOptions {
		option, _ := raw.(map[string]any)
		id, _ := option["id"].(string)
		optionIDs = append(optionIDs, id)
	}

	status, body = e.admin(http.MethodPost, "/api/v1/admin/polls/"+pollID+"/status",
		map[string]any{"status": "active"})
	require.Equal(e.t, http.StatusOK, status, body)

	return pollID, optionIDs
}

// vote проходит путь настоящего зрителя: сначала страница опроса (там выдаётся
// кука), потом сам голос.
func (e *env) vote(
	client *http.Client,
	pollID string,
	optionIDs ...string,
) (status int, decoded map[string]any) {
	e.t.Helper()

	status, body := e.do(client, http.MethodGet, "/api/v1/polls/"+pollID, nil, "")
	require.Equal(e.t, http.StatusOK, status, body)

	return e.do(client, http.MethodPost, "/api/v1/polls/"+pollID+"/vote",
		map[string]any{"option_ids": optionIDs}, "")
}

func (e *env) results(pollID string) map[string]any {
	e.t.Helper()

	status, body := e.do(e.newViewer(), http.MethodGet, "/api/v1/polls/"+pollID+"/results", nil, "")
	require.Equal(e.t, http.StatusOK, status, body)

	return body
}

func votesFor(payload map[string]any, optionID string) int64 {
	options, _ := payload["options"].([]any)
	for _, raw := range options {
		option, _ := raw.(map[string]any)
		if id, _ := option["id"].(string); id == optionID {
			votes, _ := option["votes"].(float64)

			return int64(votes)
		}
	}

	return -1
}

func TestVotingFlow(t *testing.T) {
	e := newEnv(t)
	pollID, optionIDs := e.createActivePoll("single_choice", []string{"Алиса", "Борис", "Виктор"}, 1)

	viewer := e.newViewer()
	status, body := e.vote(viewer, pollID, optionIDs[0])
	require.Equal(t, http.StatusAccepted, status, body)
	require.Equal(t, "accepted", body["outcome"])

	// Тот же зритель, тот же опрос — второй голос принимать нельзя.
	status, body = e.vote(viewer, pollID, optionIDs[1])
	require.Equal(t, http.StatusConflict, status, body)

	// Ждём сброса счётчиков из памяти в Redis.
	time.Sleep(4 * flushInterval)

	tally := e.results(pollID)
	require.Equal(t, float64(1), tally["voters"])
	require.Equal(t, int64(1), votesFor(tally, optionIDs[0]))
	require.Equal(t, int64(0), votesFor(tally, optionIDs[1]),
		"отклонённый повторный голос не должен попадать в счётчики")
}

func TestManyViewersAreCountedIndependently(t *testing.T) {
	e := newEnv(t)
	pollID, optionIDs := e.createActivePoll("ab", []string{"За", "Против"}, 1)

	const viewers = 25
	for i := range viewers {
		status, body := e.vote(e.newViewer(), pollID, optionIDs[i%2])
		require.Equal(t, http.StatusAccepted, status, body)
	}

	time.Sleep(4 * flushInterval)

	tally := e.results(pollID)
	require.Equal(t, float64(viewers), tally["voters"])
	require.Equal(t, int64(13), votesFor(tally, optionIDs[0]))
	require.Equal(t, int64(12), votesFor(tally, optionIDs[1]))
	require.Equal(t, "live", tally["source"])
}

func TestMultipleChoiceCountsVoterOnce(t *testing.T) {
	e := newEnv(t)
	pollID, optionIDs := e.createActivePoll("multiple_choice", []string{"А", "Б", "В"}, 2)

	status, body := e.vote(e.newViewer(), pollID, optionIDs[0], optionIDs[1])
	require.Equal(t, http.StatusAccepted, status, body)

	time.Sleep(4 * flushInterval)

	tally := e.results(pollID)
	// Один человек, две отметки: доли считаются от людей и дают по 100%.
	require.Equal(t, float64(1), tally["voters"])
	require.Equal(t, int64(1), votesFor(tally, optionIDs[0]))
	require.Equal(t, int64(1), votesFor(tally, optionIDs[1]))
}

func TestMultipleChoiceRejectsTooManyOptions(t *testing.T) {
	e := newEnv(t)
	pollID, optionIDs := e.createActivePoll("multiple_choice", []string{"А", "Б", "В"}, 2)

	status, body := e.vote(e.newViewer(), pollID, optionIDs[0], optionIDs[1], optionIDs[2])
	require.Equal(t, http.StatusBadRequest, status, body)
}

func TestClosedPollRejectsVotesAndFreezesResults(t *testing.T) {
	e := newEnv(t)
	pollID, optionIDs := e.createActivePoll("single_choice", []string{"А", "Б"}, 1)

	status, _ := e.vote(e.newViewer(), pollID, optionIDs[0])
	require.Equal(t, http.StatusAccepted, status)

	time.Sleep(4 * flushInterval)

	status, body := e.admin(http.MethodPost, "/api/v1/admin/polls/"+pollID+"/status",
		map[string]any{"status": "closed"})
	require.Equal(t, http.StatusOK, status, body)

	status, body = e.vote(e.newViewer(), pollID, optionIDs[0])
	require.Equal(t, http.StatusGone, status, body)

	tally := e.results(pollID)
	require.Equal(t, true, tally["final"])
	require.Equal(t, float64(1), tally["voters"])

	// Закрытый опрос переоткрыть нельзя: иначе «финальный итог» ничего не значит.
	status, body = e.admin(http.MethodPost, "/api/v1/admin/polls/"+pollID+"/status",
		map[string]any{"status": "active"})
	require.Equal(t, http.StatusConflict, status, body)
}

func TestDraftPollDoesNotAcceptVotes(t *testing.T) {
	e := newEnv(t)

	status, body := e.admin(http.MethodPost, "/api/v1/admin/polls", map[string]any{
		"title": "Черновик", "question": "Кто?", "kind": "ab", "options": []string{"А", "Б"},
	})
	require.Equal(t, http.StatusCreated, status, body)

	pollID, _ := body["id"].(string)
	rawOptions, _ := body["options"].([]any)
	firstOption, _ := rawOptions[0].(map[string]any)
	optionID, _ := firstOption["id"].(string)

	status, body = e.vote(e.newViewer(), pollID, optionID)
	require.Equal(t, http.StatusGone, status, body)
}

func TestAdminEndpointsRequireToken(t *testing.T) {
	e := newEnv(t)
	client := e.newViewer()

	cases := []struct {
		name   string
		method string
		path   string
		token  string
	}{
		{name: "без токена", method: http.MethodGet, path: "/api/v1/admin/polls"},
		{name: "чужой токен", method: http.MethodGet, path: "/api/v1/admin/polls", token: "nope"},
		{name: "создание без токена", method: http.MethodPost, path: "/api/v1/admin/polls"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := e.do(client, tc.method, tc.path, nil, tc.token)
			require.Equal(t, http.StatusUnauthorized, status)
		})
	}
}

func TestUnknownPollReturns404(t *testing.T) {
	e := newEnv(t)
	client := e.newViewer()

	status, body := e.do(client, http.MethodGet,
		"/api/v1/polls/11111111-1111-1111-1111-111111111111", nil, "")
	require.Equal(t, http.StatusNotFound, status, body)
}

func TestTimelineReturnsSavedSnapshots(t *testing.T) {
	e := newEnv(t)
	pg := boilerplate.NewPostgres(t)
	pollID, optionIDs := e.createActivePoll("ab", []string{"А", "Б"}, 1)

	status, _ := e.vote(e.newViewer(), pollID, optionIDs[0])
	require.Equal(t, http.StatusAccepted, status)

	time.Sleep(4 * flushInterval)

	// Снапшоттер в этом тесте не крутится, поэтому агрегат сохраняем напрямую:
	// проверяется именно чтение истории через API.
	parsedPollID, err := uuid.Parse(pollID)
	require.NoError(t, err)
	parsedOptionID, err := uuid.Parse(optionIDs[0])
	require.NoError(t, err)

	require.NoError(t, resultsstorage.New(pg).Save(context.Background(),
		parsedPollID, 1, map[uuid.UUID]int64{parsedOptionID: 1}))

	status, body := e.admin(http.MethodGet, "/api/v1/admin/polls/"+pollID+"/timeline", nil)
	require.Equal(t, http.StatusOK, status, body)

	points, _ := body["points"].([]any)
	require.Len(t, points, 1)

	point, _ := points[0].(map[string]any)
	require.Equal(t, float64(1), point["voters"])
}
