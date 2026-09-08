// Package poll — доменная логика опросов: валидация при создании, переходы
// состояний и раздача определения опроса на горячем пути голосования.
package poll

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
)

// Границы, за которые опрос выходить не должен. Верхние нужны не столько для
// красоты, сколько чтобы одним запросом нельзя было создать опрос с миллионом
// вариантов и раздуть счётчики.
const (
	minOptions     = 2
	maxOptions     = 20
	maxTitleLen    = 200
	maxQuestionLen = 500
	maxOptionLen   = 200
)

//go:generate mockgen -source=service.go -destination=mocks/service.go -package=mocks

// Storage — хранилище опросов.
type Storage interface {
	Create(ctx context.Context, poll dto.Poll) error
	GetByID(ctx context.Context, pollID uuid.UUID) (dto.Poll, error)
	List(ctx context.Context, limit, offset int) ([]dto.Poll, error)
	ListForSnapshot(ctx context.Context, closedGrace time.Duration) ([]uuid.UUID, error)
	SetStatus(ctx context.Context, pollID uuid.UUID, status dto.Status) error
}

// Service отдаёт опросы и управляет их жизненным циклом.
//
// Определение опроса читается на каждый голос, поэтому оно кэшируется в памяти
// процесса: при 100М зрителей поход в PostgreSQL за каждым голосом положил бы
// базу мгновенно. Кэш живёт секунду — этого хватает, чтобы закрытие опроса
// разъехалось по репликам почти сразу.
type Service struct {
	storage  Storage
	cacheTTL time.Duration
	cache    sync.Map // uuid.UUID -> *cacheEntry
	group    singleflight.Group
	now      func() time.Time
}

type cacheEntry struct {
	poll      dto.Poll
	err       error
	expiresAt time.Time
}

// New создаёт сервис опросов.
func New(storage Storage, cacheTTL time.Duration) *Service {
	return &Service{storage: storage, cacheTTL: cacheTTL, now: time.Now}
}

// Create проверяет параметры и сохраняет новый опрос в состоянии draft.
func (s *Service) Create(ctx context.Context, params dto.CreateParams) (dto.Poll, error) {
	poll, err := buildPoll(params)
	if err != nil {
		return dto.Poll{}, err
	}

	if err := s.storage.Create(ctx, poll); err != nil {
		return dto.Poll{}, fmt.Errorf("create poll: %w", err)
	}

	return poll, nil
}

// Get возвращает опрос из кэша, ходя в базу не чаще раза в cacheTTL.
func (s *Service) Get(ctx context.Context, pollID uuid.UUID) (dto.Poll, error) {
	if cached, ok := s.cache.Load(pollID); ok {
		entry, _ := cached.(*cacheEntry)
		if s.now().Before(entry.expiresAt) {
			return entry.poll, entry.err
		}
	}

	// singleflight нужен ровно на момент выхода опроса в эфир: тысячи
	// одновременных запросов на холодный кэш превратятся в один SELECT.
	result, err, _ := s.group.Do(pollID.String(), func() (any, error) {
		poll, err := s.storage.GetByID(ctx, pollID)
		if err != nil && !errors.Is(err, dto.ErrNotFound) {
			return nil, fmt.Errorf("get poll: %w", err)
		}

		// Отрицательный результат кэшируется тоже: иначе перебор случайных id
		// уходит в базу целиком.
		entry := &cacheEntry{poll: poll, err: err, expiresAt: s.now().Add(s.cacheTTL)}
		s.cache.Store(pollID, entry)

		return entry, nil
	})
	if err != nil {
		return dto.Poll{}, err
	}

	entry, _ := result.(*cacheEntry)

	return entry.poll, entry.err
}

// GetFresh читает опрос мимо кэша — для админки, где важна актуальность.
func (s *Service) GetFresh(ctx context.Context, pollID uuid.UUID) (dto.Poll, error) {
	poll, err := s.storage.GetByID(ctx, pollID)
	if err != nil {
		return dto.Poll{}, err
	}

	s.cache.Store(pollID, &cacheEntry{poll: poll, expiresAt: s.now().Add(s.cacheTTL)})

	return poll, nil
}

// List возвращает опросы для админки.
func (s *Service) List(ctx context.Context, limit, offset int) ([]dto.Poll, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	if offset < 0 {
		offset = 0
	}

	polls, err := s.storage.List(ctx, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list polls: %w", err)
	}

	return polls, nil
}

// ListForSnapshot возвращает опросы, за которыми должен следить снапшоттер.
func (s *Service) ListForSnapshot(ctx context.Context, closedGrace time.Duration) ([]uuid.UUID, error) {
	pollIDs, err := s.storage.ListForSnapshot(ctx, closedGrace)
	if err != nil {
		return nil, fmt.Errorf("list polls for snapshot: %w", err)
	}

	return pollIDs, nil
}

// SetStatus переводит опрос в новое состояние.
func (s *Service) SetStatus(ctx context.Context, pollID uuid.UUID, status dto.Status) (dto.Poll, error) {
	if !status.Valid() {
		return dto.Poll{}, fmt.Errorf("%w: unknown status %q", dto.ErrValidation, status)
	}

	current, err := s.GetFresh(ctx, pollID)
	if err != nil {
		return dto.Poll{}, err
	}

	if !canTransition(current.Status, status) {
		return dto.Poll{}, fmt.Errorf("%w: %s -> %s", dto.ErrInvalidStatus, current.Status, status)
	}

	if err := s.storage.SetStatus(ctx, pollID, status); err != nil {
		return dto.Poll{}, fmt.Errorf("set poll status: %w", err)
	}

	// Локальный кэш сбрасываем сразу; на других репликах опрос протухнет сам
	// в пределах cacheTTL.
	s.cache.Delete(pollID)
	current.Status = status

	return current, nil
}

// canTransition разрешает только движение вперёд по жизненному циклу:
// закрытый опрос переоткрыть нельзя, иначе результаты перестают быть финальными.
func canTransition(from, to dto.Status) bool {
	switch from {
	case dto.StatusDraft:
		return to == dto.StatusActive || to == dto.StatusClosed
	case dto.StatusActive:
		return to == dto.StatusClosed
	case dto.StatusClosed:
		return false
	default:
		return false
	}
}

func buildPoll(params dto.CreateParams) (dto.Poll, error) {
	title := strings.TrimSpace(params.Title)
	question := strings.TrimSpace(params.Question)

	if title == "" || len(title) > maxTitleLen {
		return dto.Poll{}, fmt.Errorf("%w: title must be 1..%d characters", dto.ErrValidation, maxTitleLen)
	}

	if question == "" || len(question) > maxQuestionLen {
		return dto.Poll{}, fmt.Errorf("%w: question must be 1..%d characters", dto.ErrValidation, maxQuestionLen)
	}

	if !params.Kind.Valid() {
		return dto.Poll{}, fmt.Errorf("%w: unknown kind %q", dto.ErrValidation, params.Kind)
	}

	options, err := buildOptions(params.Kind, params.Options)
	if err != nil {
		return dto.Poll{}, err
	}

	maxChoices, err := resolveMaxChoices(params.Kind, params.MaxChoices, len(options))
	if err != nil {
		return dto.Poll{}, err
	}

	if params.OpensAt != nil && params.ClosesAt != nil && !params.OpensAt.Before(*params.ClosesAt) {
		return dto.Poll{}, fmt.Errorf("%w: opens_at must be before closes_at", dto.ErrValidation)
	}

	now := time.Now().UTC()

	return dto.Poll{
		ID:         uuid.New(),
		Title:      title,
		Question:   question,
		Kind:       params.Kind,
		Status:     dto.StatusDraft,
		MaxChoices: maxChoices,
		OpensAt:    params.OpensAt,
		ClosesAt:   params.ClosesAt,
		CreatedAt:  now,
		UpdatedAt:  now,
		Options:    options,
	}, nil
}

func buildOptions(kind dto.Kind, texts []string) ([]dto.Option, error) {
	options := make([]dto.Option, 0, len(texts))
	seen := make(map[string]struct{}, len(texts))

	for _, raw := range texts {
		text := strings.TrimSpace(raw)
		if text == "" || len(text) > maxOptionLen {
			return nil, fmt.Errorf("%w: option must be 1..%d characters", dto.ErrValidation, maxOptionLen)
		}

		if _, duplicate := seen[text]; duplicate {
			return nil, fmt.Errorf("%w: duplicate option %q", dto.ErrValidation, text)
		}
		seen[text] = struct{}{}

		options = append(options, dto.Option{ID: uuid.New(), Position: len(options), Text: text})
	}

	// a/b — это ровно две кнопки на экране, поэтому третий вариант означает,
	// что автор опроса ошибся типом.
	if kind == dto.KindAB && len(options) != 2 {
		return nil, fmt.Errorf("%w: ab poll requires exactly 2 options", dto.ErrValidation)
	}

	if len(options) < minOptions || len(options) > maxOptions {
		return nil, fmt.Errorf("%w: poll requires %d..%d options", dto.ErrValidation, minOptions, maxOptions)
	}

	return options, nil
}

func resolveMaxChoices(kind dto.Kind, requested, optionCount int) (int, error) {
	if kind != dto.KindMultipleChoice {
		if requested > 1 {
			return 0, fmt.Errorf("%w: max_choices > 1 requires multiple_choice", dto.ErrValidation)
		}

		return 1, nil
	}

	if requested <= 0 {
		return optionCount, nil
	}

	if requested > optionCount {
		return 0, fmt.Errorf("%w: max_choices exceeds option count", dto.ErrValidation)
	}

	return requested, nil
}
