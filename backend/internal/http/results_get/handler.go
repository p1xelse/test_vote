// Package resultsget — ручка чтения обезличенных результатов опроса.
package resultsget

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/dneumoin/test_vote/backend/internal/http/apidto"
	"github.com/dneumoin/test_vote/backend/internal/http/apierr"
	resultsdto "github.com/dneumoin/test_vote/backend/internal/service/results/dto"
)

// Results отдаёт агрегаты по опросу.
type Results interface {
	Get(ctx context.Context, pollID uuid.UUID) (resultsdto.Results, error)
}

// Handler — ручка GET /api/v1/polls/:poll_id/results.
type Handler struct {
	results Results
}

// New создаёт ручку результатов.
func New(results Results) *Handler {
	return &Handler{results: results}
}

// Handle отдаёт текущие результаты.
func (h *Handler) Handle(c echo.Context) error {
	pollID, err := uuid.Parse(c.Param("poll_id"))
	if err != nil {
		return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "invalid poll id")
	}

	results, err := h.results.Get(c.Request().Context(), pollID)
	if err != nil {
		return apierr.FromDomain(err)
	}

	// Пока опрос идёт, цифры меняются каждую секунду; закрытый опрос можно
	// кэшировать надолго.
	if results.Final {
		c.Response().Header().Set("Cache-Control", "public, max-age=60")
	} else {
		c.Response().Header().Set("Cache-Control", "public, max-age=1")
	}

	return c.JSON(http.StatusOK, apidto.FromResults(results))
}
