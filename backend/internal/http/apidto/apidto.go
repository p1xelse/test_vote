// Package apidto — представление опросов и результатов в HTTP-ответах.
//
// Отдельный слой нужен, чтобы форма ответа не менялась случайно вслед за
// доменными структурами: клиенты и фронтенд завязаны именно на неё.
package apidto

import (
	"time"

	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
	resultsdto "github.com/dneumoin/test_vote/backend/internal/service/results/dto"
)

// Option — вариант ответа.
type Option struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Text     string `json:"text"`
}

// Poll — определение опроса.
type Poll struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Question   string     `json:"question"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	MaxChoices int        `json:"max_choices"`
	OpensAt    *time.Time `json:"opens_at,omitempty"`
	ClosesAt   *time.Time `json:"closes_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	// AcceptsVotes избавляет фронтенд от необходимости самому сопоставлять
	// статус с окном opens_at/closes_at.
	AcceptsVotes bool     `json:"accepts_votes"`
	Options      []Option `json:"options"`
}

// FromPoll переводит доменный опрос в ответ API.
func FromPoll(poll polldto.Poll, now time.Time) Poll {
	options := make([]Option, 0, len(poll.Options))
	for _, option := range poll.Options {
		options = append(options, Option{
			ID:       option.ID.String(),
			Position: option.Position,
			Text:     option.Text,
		})
	}

	return Poll{
		ID:           poll.ID.String(),
		Title:        poll.Title,
		Question:     poll.Question,
		Kind:         string(poll.Kind),
		Status:       string(poll.Status),
		MaxChoices:   poll.MaxChoices,
		OpensAt:      poll.OpensAt,
		ClosesAt:     poll.ClosesAt,
		CreatedAt:    poll.CreatedAt,
		AcceptsVotes: poll.AcceptsVotesAt(now),
		Options:      options,
	}
}

// OptionResult — результат по варианту ответа.
type OptionResult struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Text     string `json:"text"`
	Votes    int64  `json:"votes"`
	// Share — доля проголосовавших за этот вариант, 0..1.
	Share float64 `json:"share"`
}

// Results — обезличенные результаты опроса.
type Results struct {
	PollID   string `json:"poll_id"`
	Title    string `json:"title"`
	Question string `json:"question"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	// Voters — число проголосовавших; в multiple_choice оно меньше суммы
	// голосов по вариантам.
	Voters  int64          `json:"voters"`
	Options []OptionResult `json:"options"`
	// Final=true — опрос закрыт, цифры окончательные.
	Final bool `json:"final"`
	// Source: live — счётчики из Redis, persisted — сохранённый итог.
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}

// FromResults переводит доменные результаты в ответ API.
func FromResults(results resultsdto.Results) Results {
	options := make([]OptionResult, 0, len(results.Options))
	for _, option := range results.Options {
		options = append(options, OptionResult{
			ID:       option.OptionID.String(),
			Position: option.Position,
			Text:     option.Text,
			Votes:    option.Votes,
			Share:    option.Share,
		})
	}

	return Results{
		PollID:     results.PollID.String(),
		Title:      results.Title,
		Question:   results.Question,
		Kind:       string(results.Kind),
		Status:     string(results.Status),
		Voters:     results.Voters,
		Options:    options,
		Final:      results.Final,
		Source:     string(results.Source),
		ObservedAt: results.ObservedAt,
	}
}
