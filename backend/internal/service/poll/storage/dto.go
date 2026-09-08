package storage

import (
	"time"

	"github.com/google/uuid"

	"github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
)

// pollRow — строка выборки из polls вместе с вариантами, собранными в JSON.
type pollRow struct {
	id         uuid.UUID
	title      string
	question   string
	kind       string
	status     string
	maxChoices int16
	opensAt    *time.Time
	closesAt   *time.Time
	createdAt  time.Time
	updatedAt  time.Time
	options    []byte
}

// optionRow — вариант ответа в том виде, в каком его отдаёт json_agg.
type optionRow struct {
	ID       uuid.UUID `json:"id"`
	Position int       `json:"position"`
	Text     string    `json:"text"`
}

func (r *pollRow) scanTargets() []any {
	return []any{
		&r.id, &r.title, &r.question, &r.kind, &r.status, &r.maxChoices,
		&r.opensAt, &r.closesAt, &r.createdAt, &r.updatedAt, &r.options,
	}
}

func (r *pollRow) toDomain(options []optionRow) dto.Poll {
	poll := dto.Poll{
		ID:         r.id,
		Title:      r.title,
		Question:   r.question,
		Kind:       dto.Kind(r.kind),
		Status:     dto.Status(r.status),
		MaxChoices: int(r.maxChoices),
		OpensAt:    r.opensAt,
		ClosesAt:   r.closesAt,
		CreatedAt:  r.createdAt,
		UpdatedAt:  r.updatedAt,
		Options:    make([]dto.Option, 0, len(options)),
	}

	for _, option := range options {
		poll.Options = append(poll.Options, dto.Option{
			ID:       option.ID,
			Position: option.Position,
			Text:     option.Text,
		})
	}

	return poll
}
