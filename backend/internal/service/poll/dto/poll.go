// Package dto описывает опрос так, как его видит доменный слой.
package dto

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Ошибки домена опросов.
var (
	ErrNotFound      = errors.New("poll not found")
	ErrValidation    = errors.New("invalid poll")
	ErrInvalidStatus = errors.New("invalid poll status transition")
)

// Kind — тип вопроса.
type Kind string

// Поддерживаемые типы вопросов.
const (
	// KindSingleChoice — один вариант из нескольких.
	KindSingleChoice Kind = "single_choice"
	// KindMultipleChoice — до MaxChoices вариантов.
	KindMultipleChoice Kind = "multiple_choice"
	// KindAB — частный случай single_choice ровно с двумя вариантами.
	KindAB Kind = "ab"
)

// Valid сообщает, известен ли такой тип вопроса.
func (k Kind) Valid() bool {
	switch k {
	case KindSingleChoice, KindMultipleChoice, KindAB:
		return true
	default:
		return false
	}
}

// Status — состояние опроса.
type Status string

// Состояния опроса.
const (
	// StatusDraft — создан, но голоса не принимает.
	StatusDraft Status = "draft"
	// StatusActive — идёт голосование.
	StatusActive Status = "active"
	// StatusClosed — голосование завершено, результаты финальные.
	StatusClosed Status = "closed"
)

// Valid сообщает, известно ли такое состояние.
func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusActive, StatusClosed:
		return true
	default:
		return false
	}
}

// Option — вариант ответа.
type Option struct {
	ID       uuid.UUID
	Position int
	Text     string
}

// Poll — опрос вместе с вариантами ответа.
type Poll struct {
	ID         uuid.UUID
	Title      string
	Question   string
	Kind       Kind
	Status     Status
	MaxChoices int
	OpensAt    *time.Time
	ClosesAt   *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Options    []Option
}

// AcceptsVotesAt сообщает, принимает ли опрос голоса в момент now.
//
// Помимо статуса учитывается окно opens_at/closes_at: ролик в эфире идёт по
// расписанию, и админ не обязан закрывать опрос руками ровно в срок.
func (p *Poll) AcceptsVotesAt(now time.Time) bool {
	if p.Status != StatusActive {
		return false
	}

	if p.OpensAt != nil && now.Before(*p.OpensAt) {
		return false
	}

	if p.ClosesAt != nil && !now.Before(*p.ClosesAt) {
		return false
	}

	return true
}

// HasOption сообщает, принадлежит ли вариант этому опросу.
func (p *Poll) HasOption(optionID uuid.UUID) bool {
	for _, option := range p.Options {
		if option.ID == optionID {
			return true
		}
	}

	return false
}

// CreateParams — то, что приходит из админки при создании опроса.
type CreateParams struct {
	Title      string
	Question   string
	Kind       Kind
	MaxChoices int
	OpensAt    *time.Time
	ClosesAt   *time.Time
	Options    []string
}
