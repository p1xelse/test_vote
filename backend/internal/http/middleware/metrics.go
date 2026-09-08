package middleware

import (
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/dneumoin/test_vote/backend/internal/observability"
)

// Metrics считает запросы и время ответа по маршрутам.
//
// Меткой служит шаблон маршрута, а не сам путь: иначе каждый id опроса создал
// бы отдельную серию и разнёс бы Prometheus.
func Metrics(metrics *observability.Metrics) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)

			status := c.Response().Status
			if err != nil {
				if httpErr, ok := err.(*echo.HTTPError); ok {
					status = httpErr.Code
				} else {
					status = 500
				}
			}

			route := c.Path()
			if route == "" {
				route = "unmatched"
			}

			metrics.HTTPRequests.WithLabelValues(route, strconv.Itoa(status)).Inc()
			metrics.HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())

			return err
		}
	}
}
