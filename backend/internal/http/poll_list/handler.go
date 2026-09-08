// Package polllist — админская ручка со списком опросов.
package polllist

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/dneumoin/test_vote/backend/internal/http/apidto"
	"github.com/dneumoin/test_vote/backend/internal/http/apierr"
	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
)

// Polls отдаёт список опросов.
type Polls interface {
	List(ctx context.Context, limit, offset int) ([]polldto.Poll, error)
}

// Handler — ручка GET /api/v1/admin/polls.
type Handler struct {
	polls Polls
}

// New создаёт ручку списка опросов.
func New(polls Polls) *Handler {
	return &Handler{polls: polls}
}

// Out — тело ответа.
type Out struct {
	Polls []apidto.Poll `json:"polls"`
}

// Handle отдаёт опросы, свежие сверху.
func (h *Handler) Handle(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))

	polls, err := h.polls.List(c.Request().Context(), limit, offset)
	if err != nil {
		return apierr.FromDomain(err)
	}

	now := time.Now()
	out := Out{Polls: make([]apidto.Poll, 0, len(polls))}
	for i := range polls {
		out.Polls = append(out.Polls, apidto.FromPoll(polls[i], now))
	}

	return c.JSON(http.StatusOK, out)
}
