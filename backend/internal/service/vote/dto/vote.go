// Package dto описывает вход и выход операции голосования.
package dto

import (
	"errors"

	"github.com/google/uuid"

	"github.com/dneumoin/test_vote/backend/internal/dedup"
)

// Ошибки голосования.
var (
	ErrPollNotAccepting = errors.New("poll is not accepting votes")
	ErrInvalidOption    = errors.New("option does not belong to poll")
	ErrChoiceCount      = errors.New("wrong number of choices")
)

// Outcome — чем закончилась попытка проголосовать.
type Outcome string

// Возможные исходы.
const (
	// OutcomeAccepted — голос учтён.
	OutcomeAccepted Outcome = "accepted"
	// OutcomeDuplicate — этот зритель уже голосовал в этом опросе.
	OutcomeDuplicate Outcome = "duplicate"
)

// CastParams — запрос на голос.
type CastParams struct {
	PollID    uuid.UUID
	OptionIDs []uuid.UUID
	Identity  dedup.Identity
}

// CastResult — результат попытки проголосовать.
type CastResult struct {
	Outcome Outcome
}
