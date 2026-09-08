// Package httpapi собирает HTTP-слой сервиса: маршруты, прослойки и раздачу
// статики фронтенда.
package httpapi

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"net/http/pprof"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dneumoin/test_vote/backend/internal/http/apierr"
	"github.com/dneumoin/test_vote/backend/internal/http/middleware"
	pollcreate "github.com/dneumoin/test_vote/backend/internal/http/poll_create"
	pollget "github.com/dneumoin/test_vote/backend/internal/http/poll_get"
	polllist "github.com/dneumoin/test_vote/backend/internal/http/poll_list"
	pollstatusset "github.com/dneumoin/test_vote/backend/internal/http/poll_status_set"
	resultsget "github.com/dneumoin/test_vote/backend/internal/http/results_get"
	resultstimelineget "github.com/dneumoin/test_vote/backend/internal/http/results_timeline_get"
	votecast "github.com/dneumoin/test_vote/backend/internal/http/vote_cast"
	"github.com/dneumoin/test_vote/backend/internal/observability"
)

// bodyLimit — тела запросов здесь крошечные, и большой лимит только открывал бы
// возможность занять память сервиса мусором.
const bodyLimit = "8K"

// Deps — всё, что нужно HTTP-слою.
type Deps struct {
	Polls       pollget.Polls
	PollsAdmin  AdminPolls
	Votes       votecast.Voter
	Results     ResultsService
	Voters      Voters
	RateLimiter *middleware.RateLimiter
	Metrics     *observability.Metrics
	Logger      *slog.Logger
	AdminToken  string
	FrontendDir string
}

// AdminPolls — операции админки над опросами.
type AdminPolls interface {
	pollcreate.Polls
	polllist.Polls
	pollstatusset.Polls
}

// ResultsService — чтение результатов и их динамики.
type ResultsService interface {
	resultsget.Results
	resultstimelineget.Results
}

// Voters опознаёт зрителя и выдаёт ему куку.
type Voters interface {
	votecast.Identifier
	pollget.CookieIssuer
}

// New собирает echo с маршрутами сервиса.
func New(deps Deps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = errorHandler(deps.Logger)
	// За балансировщиком реальный адрес зрителя приходит в X-Forwarded-For;
	// доверяем заголовку только от приватных сетей.
	e.IPExtractor = echo.ExtractIPFromXFFHeader()

	e.Use(
		echomw.Recover(),
		echomw.RequestID(),
		echomw.BodyLimit(bodyLimit),
		middleware.Metrics(deps.Metrics),
	)

	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	api := e.Group("/api/v1")

	api.GET("/polls/:poll_id", pollget.New(deps.Polls, deps.Voters).Handle)
	api.GET("/polls/:poll_id/results", resultsget.New(deps.Results).Handle)
	// Лимитер стоит только на голосовании: чтение раздаётся из кэша и стоит
	// дёшево, а вот запись имеет смысл прикрыть от скрипта в цикле.
	api.POST("/polls/:poll_id/vote",
		votecast.New(deps.Votes, deps.Voters).Handle,
		deps.RateLimiter.Middleware(),
	)

	admin := api.Group("/admin", adminAuth(deps.AdminToken))
	admin.GET("/polls", polllist.New(deps.PollsAdmin).Handle)
	admin.POST("/polls", pollcreate.New(deps.PollsAdmin).Handle)
	admin.POST("/polls/:poll_id/status", pollstatusset.New(deps.PollsAdmin).Handle)
	admin.GET("/polls/:poll_id/results", resultsget.New(deps.Results).Handle)
	admin.GET("/polls/:poll_id/timeline", resultstimelineget.New(deps.Results).Handle)

	if deps.FrontendDir != "" {
		e.Static("/", deps.FrontendDir)
	}

	return e
}

// NewDebugServer поднимает метрики и pprof отдельным слушателем: наружу такое
// не выставляют, а внутри кластера оно должно быть доступно всегда.
func NewDebugServer(metrics *observability.Metrics) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.GET("/metrics", echo.WrapHandler(promhttp.HandlerFor(
		metrics.Registry(),
		promhttp.HandlerOpts{},
	)))

	e.GET("/debug/pprof/*", echo.WrapHandler(http.HandlerFunc(pprof.Index)))
	e.GET("/debug/pprof/profile", echo.WrapHandler(http.HandlerFunc(pprof.Profile)))
	e.GET("/debug/pprof/trace", echo.WrapHandler(http.HandlerFunc(pprof.Trace)))
	e.GET("/debug/pprof/cmdline", echo.WrapHandler(http.HandlerFunc(pprof.Cmdline)))
	e.GET("/debug/pprof/symbol", echo.WrapHandler(http.HandlerFunc(pprof.Symbol)))

	return e
}

// adminAuth — статический токен в заголовке Authorization.
//
// Для тестового задания этого достаточно: админка внутренняя, и полноценная
// аутентификация с пользователями и ролями к задаче отношения не имеет.
func adminAuth(token string) echo.MiddlewareFunc {
	expected := []byte(token)

	return echomw.KeyAuthWithConfig(echomw.KeyAuthConfig{
		KeyLookup:  "header:Authorization",
		AuthScheme: "Bearer",
		Validator: func(key string, _ echo.Context) (bool, error) {
			// Сравнение за постоянное время: иначе токен подбирается по времени ответа.
			return subtle.ConstantTimeCompare([]byte(key), expected) == 1, nil
		},
		ErrorHandler: func(_ error, _ echo.Context) error {
			return apierr.New(http.StatusUnauthorized, apierr.CodeUnauthorized, "admin token required")
		},
	})
}

// errorHandler приводит любую ошибку к общему формату и логирует наши 5xx.
func errorHandler(logger *slog.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		httpErr, ok := err.(*echo.HTTPError)
		if !ok {
			httpErr = apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "internal error")
		}

		if httpErr.Code >= http.StatusInternalServerError {
			logger.Error("request failed",
				slog.String("method", c.Request().Method),
				slog.String("path", c.Path()),
				slog.Any("error", err),
			)
		}

		body, ok := httpErr.Message.(apierr.Response)
		if !ok {
			// Ошибки, пришедшие из самого echo (404 на неизвестный маршрут,
			// 405 и подобные), приводим к тому же виду.
			body = apierr.Response{Error: apierr.Body{
				Code:    http.StatusText(httpErr.Code),
				Message: http.StatusText(httpErr.Code),
			}}
		}

		if respErr := c.JSON(httpErr.Code, body); respErr != nil {
			logger.Error("write error response", slog.Any("error", respErr))
		}
	}
}
