// Package pollcreate — админская ручка создания опроса.
package pollcreate

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/dneumoin/test_vote/backend/internal/http/apidto"
	"github.com/dneumoin/test_vote/backend/internal/http/apierr"
	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
)

// Polls создаёт опросы.
type Polls interface {
	Create(ctx context.Context, params polldto.CreateParams) (polldto.Poll, error)
}

// Handler — ручка POST /api/v1/admin/polls.
type Handler struct {
	polls Polls
}

// New создаёт ручку создания опроса.
func New(polls Polls) *Handler {
	return &Handler{polls: polls}
}

// In — тело запроса.
type In struct {
	Title    string `json:"title"`
	Question string `json:"question"`
	// Kind: single_choice, multiple_choice или ab.
	Kind string `json:"kind"`
	// MaxChoices имеет смысл только для multiple_choice; 0 означает «без ограничения».
	MaxChoices int        `json:"max_choices"`
	OpensAt    *time.Time `json:"opens_at"`
	ClosesAt   *time.Time `json:"closes_at"`
	Options    []string   `json:"options"`
}

// Handle создаёт опрос в состоянии draft.
func (h *Handler) Handle(c echo.Context) error {
	var in In
	if err := c.Bind(&in); err != nil {
		return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "malformed request body")
	}

	poll, err := h.polls.Create(c.Request().Context(), polldto.CreateParams{
		Title:      in.Title,
		Question:   in.Question,
		Kind:       polldto.Kind(in.Kind),
		MaxChoices: in.MaxChoices,
		OpensAt:    in.OpensAt,
		ClosesAt:   in.ClosesAt,
		Options:    in.Options,
	})
	if err != nil {
		return apierr.FromDomain(err)
	}

	return c.JSON(http.StatusCreated, apidto.FromPoll(poll, time.Now()))
}
