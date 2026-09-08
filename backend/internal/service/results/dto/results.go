// Package dto описывает обезличенные результаты опроса.
//
// Ничего, что связывало бы голос с конкретным зрителем, здесь нет и быть не
// может: сервис хранит только целые числа.
package dto

import (
	"time"

	"github.com/google/uuid"

	polldto "github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
)

// Source — откуда взяты цифры.
type Source string

// Возможные источники результатов.
const (
	// SourceLive — счётчики из Redis, актуальные на текущую секунду.
	SourceLive Source = "live"
	// SourcePersisted — агрегаты из PostgreSQL: опрос давно закрыт и счётчики
	// в Redis уже истекли.
	SourcePersisted Source = "persisted"
)

// OptionResult — результат по одному варианту ответа.
type OptionResult struct {
	OptionID uuid.UUID
	Position int
	Text     string
	Votes    int64
	// Share — доля от числа проголосовавших, 0..1.
	Share float64
}

// Results — полная картина по опросу.
type Results struct {
	PollID   uuid.UUID
	Title    string
	Question string
	Kind     polldto.Kind
	Status   polldto.Status
	// Voters — число проголосовавших. В multiple_choice оно меньше суммы
	// голосов по вариантам, и проценты считаются именно от него.
	Voters  int64
	Options []OptionResult
	// Final=true означает, что опрос закрыт и цифры больше не изменятся.
	Final      bool
	Source     Source
	ObservedAt time.Time
}

// TimelinePoint — снимок результатов на момент времени.
type TimelinePoint struct {
	CapturedAt time.Time
	Voters     int64
	Counts     map[uuid.UUID]int64
}
