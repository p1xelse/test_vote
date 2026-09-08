// Package votecast — ручка анонимного голосования.
package votecast

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/dneumoin/test_vote/backend/internal/dedup"
	"github.com/dneumoin/test_vote/backend/internal/http/apierr"
	votedto "github.com/dneumoin/test_vote/backend/internal/service/vote/dto"
)

// maxChoicesPerRequest — верхняя граница на размер запроса, чтобы не разбирать
// заведомо мусорный список из тысяч идентификаторов.
const maxChoicesPerRequest = 32

//go:generate mockgen -source=handler.go -destination=mocks/handler.go -package=mocks

// Voter принимает голоса.
type Voter interface {
	Cast(ctx context.Context, params votedto.CastParams) (votedto.CastResult, error)
}

// Identifier опознаёт зрителя по запросу.
type Identifier interface {
	Identify(c echo.Context) dedup.Identity
}

// Handler — ручка POST /api/v1/polls/:poll_id/vote.
type Handler struct {
	votes  Voter
	voters Identifier
}

// New создаёт ручку голосования.
func New(votes Voter, voters Identifier) *Handler {
	return &Handler{votes: votes, voters: voters}
}

// In — тело запроса.
type In struct {
	OptionIDs []string `json:"option_ids"`
}

// Out — тело ответа.
type Out struct {
	Outcome string `json:"outcome"`
}

// Handle принимает голос зрителя.
func (h *Handler) Handle(c echo.Context) error {
	pollID, err := uuid.Parse(c.Param("poll_id"))
	if err != nil {
		return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "invalid poll id")
	}

	var in In
	if err := c.Bind(&in); err != nil {
		return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "malformed request body")
	}

	if len(in.OptionIDs) == 0 || len(in.OptionIDs) > maxChoicesPerRequest {
		return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "wrong number of option ids")
	}

	optionIDs := make([]uuid.UUID, 0, len(in.OptionIDs))
	for _, raw := range in.OptionIDs {
		optionID, err := uuid.Parse(raw)
		if err != nil {
			return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "invalid option id")
		}

		optionIDs = append(optionIDs, optionID)
	}

	result, err := h.votes.Cast(c.Request().Context(), votedto.CastParams{
		PollID:    pollID,
		OptionIDs: optionIDs,
		Identity:  h.voters.Identify(c),
	})
	if err != nil {
		return apierr.FromDomain(err)
	}

	if result.Outcome == votedto.OutcomeDuplicate {
		return apierr.New(http.StatusConflict, apierr.CodeAlreadyVoted, "you have already voted in this poll")
	}

	// 202, а не 200: голос принят и гарантированно учтён, но в общий счётчик
	// он попадёт вместе с ближайшим сбросом, а не прямо сейчас.
	return c.JSON(http.StatusAccepted, Out{Outcome: string(result.Outcome)})
}
