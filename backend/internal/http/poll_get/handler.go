// Package pollget — публичная ручка чтения опроса.
package pollget

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/dneumoin/test_vote/backend/internal/http/apidto"
	"github.com/dneumoin/test_vote/backend/internal/http/apierr"
	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
)

// Polls отдаёт определение опроса.
type Polls interface {
	Get(ctx context.Context, pollID uuid.UUID) (polldto.Poll, error)
}

// CookieIssuer выдаёт зрителю куку для дедупликации.
type CookieIssuer interface {
	Issue(c echo.Context)
}

// Handler — ручка GET /api/v1/polls/:poll_id.
type Handler struct {
	polls   Polls
	cookies CookieIssuer
}

// New создаёт ручку чтения опроса.
func New(polls Polls, cookies CookieIssuer) *Handler {
	return &Handler{polls: polls, cookies: cookies}
}

// Handle отдаёт опрос и заодно выдаёт зрителю куку.
func (h *Handler) Handle(c echo.Context) error {
	pollID, err := uuid.Parse(c.Param("poll_id"))
	if err != nil {
		return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "invalid poll id")
	}

	poll, err := h.polls.Get(c.Request().Context(), pollID)
	if err != nil {
		return apierr.FromDomain(err)
	}

	// Кука выдаётся здесь, а не при голосовании: зритель сначала открывает
	// страницу по QR-коду, так что к моменту нажатия кнопки она уже есть.
	h.cookies.Issue(c)

	// Определение опроса можно недолго держать в CDN — оно одинаково для всех.
	c.Response().Header().Set("Cache-Control", "public, max-age=1")

	return c.JSON(http.StatusOK, apidto.FromPoll(poll, time.Now()))
}
