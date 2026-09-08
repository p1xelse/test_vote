// Package pollstatusset — админская ручка запуска и закрытия опроса.
package pollstatusset

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

// Polls управляет состоянием опроса.
type Polls interface {
	SetStatus(ctx context.Context, pollID uuid.UUID, status polldto.Status) (polldto.Poll, error)
}

// Handler — ручка POST /api/v1/admin/polls/:poll_id/status.
type Handler struct {
	polls Polls
}

// New создаёт ручку смены статуса.
func New(polls Polls) *Handler {
	return &Handler{polls: polls}
}

// In — тело запроса.
type In struct {
	// Status: active — открыть голосование, closed — закрыть.
	Status string `json:"status"`
}

// Handle переводит опрос в новое состояние.
func (h *Handler) Handle(c echo.Context) error {
	pollID, err := uuid.Parse(c.Param("poll_id"))
	if err != nil {
		return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "invalid poll id")
	}

	var in In
	if err := c.Bind(&in); err != nil {
		return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "malformed request body")
	}

	poll, err := h.polls.SetStatus(c.Request().Context(), pollID, polldto.Status(in.Status))
	if err != nil {
		return apierr.FromDomain(err)
	}

	return c.JSON(http.StatusOK, apidto.FromPoll(poll, time.Now()))
}
