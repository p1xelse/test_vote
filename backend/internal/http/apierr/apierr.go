// Package apierr приводит ошибки домена к единому виду в HTTP-ответе.
package apierr

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
	votedto "github.com/dneumoin/test_vote/backend/internal/service/vote/dto"
)

// Body — тело ошибки. Машиночитаемый code важнее текста: по нему фронтенд
// отличает «уже голосовал» от «опрос закрыт», не разбирая сообщение.
type Body struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Response — конверт ошибки.
type Response struct {
	Error Body `json:"error"`
}

// Коды ошибок, попадающие в ответ.
const (
	CodeNotFound         = "not_found"
	CodeValidation       = "validation_error"
	CodeAlreadyVoted     = "already_voted"
	CodePollNotAccepting = "poll_not_accepting"
	CodeInvalidStatus    = "invalid_status_transition"
	CodeUnauthorized     = "unauthorized"
	CodeRateLimited      = "rate_limited"
	CodeInternal         = "internal_error"
)

// New собирает HTTP-ошибку с нужным кодом.
func New(status int, code, message string) *echo.HTTPError {
	return &echo.HTTPError{Code: status, Message: Response{Error: Body{Code: code, Message: message}}}
}

// FromDomain переводит доменную ошибку в HTTP-ответ.
//
// Всё, что сюда не попало, считается нашей ошибкой и превращается в 500 —
// текст наружу при этом не уходит.
func FromDomain(err error) *echo.HTTPError {
	switch {
	case errors.Is(err, polldto.ErrNotFound):
		return New(http.StatusNotFound, CodeNotFound, "poll not found")

	case errors.Is(err, polldto.ErrValidation):
		return New(http.StatusBadRequest, CodeValidation, err.Error())

	case errors.Is(err, polldto.ErrInvalidStatus):
		return New(http.StatusConflict, CodeInvalidStatus, err.Error())

	case errors.Is(err, votedto.ErrPollNotAccepting):
		// 410: опрос был, но окно голосования закрылось.
		return New(http.StatusGone, CodePollNotAccepting, "poll is not accepting votes")

	case errors.Is(err, votedto.ErrInvalidOption), errors.Is(err, votedto.ErrChoiceCount):
		return New(http.StatusBadRequest, CodeValidation, err.Error())

	default:
		return New(http.StatusInternalServerError, CodeInternal, "internal error")
	}
}
