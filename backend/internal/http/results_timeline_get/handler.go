// Package resultstimelineget — админская ручка с динамикой голосования.
package resultstimelineget

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/dneumoin/test_vote/backend/internal/http/apierr"
	resultsdto "github.com/dneumoin/test_vote/backend/internal/service/results/dto"
)

// Results отдаёт историю снимков результатов.
type Results interface {
	Timeline(ctx context.Context, pollID uuid.UUID, limit int) ([]resultsdto.TimelinePoint, error)
}

// Handler — ручка GET /api/v1/admin/polls/:poll_id/timeline.
type Handler struct {
	results Results
}

// New создаёт ручку динамики результатов.
func New(results Results) *Handler {
	return &Handler{results: results}
}

// Point — одна точка динамики.
type Point struct {
	CapturedAt time.Time `json:"captured_at"`
	Voters     int64     `json:"voters"`
	// Counts — голоса по идентификаторам вариантов.
	Counts map[string]int64 `json:"counts"`
}

// Out — тело ответа.
type Out struct {
	Points []Point `json:"points"`
}

// Handle отдаёт снимки результатов от старых к свежим.
func (h *Handler) Handle(c echo.Context) error {
	pollID, err := uuid.Parse(c.Param("poll_id"))
	if err != nil {
		return apierr.New(http.StatusBadRequest, apierr.CodeValidation, "invalid poll id")
	}

	limit, _ := strconv.Atoi(c.QueryParam("limit"))

	points, err := h.results.Timeline(c.Request().Context(), pollID, limit)
	if err != nil {
		return apierr.FromDomain(err)
	}

	out := Out{Points: make([]Point, 0, len(points))}
	for _, point := range points {
		counts := make(map[string]int64, len(point.Counts))
		for optionID, votes := range point.Counts {
			counts[optionID.String()] = votes
		}

		out.Points = append(out.Points, Point{
			CapturedAt: point.CapturedAt,
			Voters:     point.Voters,
			Counts:     counts,
		})
	}

	return c.JSON(http.StatusOK, out)
}
