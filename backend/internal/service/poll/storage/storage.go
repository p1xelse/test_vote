// Package storage хранит опросы и варианты ответа в PostgreSQL.
//
// Нагрузка здесь низкая и предсказуемая: опросы создаёт админ, а читаются они
// через кэш в памяти сервиса. Всё, что связано с потоком голосов, живёт в
// Redis и в этот пакет не заходит.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dneumoin/test_vote/backend/internal/pgstorage"
	"github.com/dneumoin/test_vote/backend/internal/service/poll/dto"
)

// selectColumns собирает опрос вместе с вариантами за один запрос: отдельный
// поход за вариантами не нужен, а порядок задаёт position.
const selectColumns = `
    p.id, p.title, p.question, p.kind::text, p.status::text, p.max_choices,
    p.opens_at, p.closes_at, p.created_at, p.updated_at,
    COALESCE(
        (
            SELECT json_agg(
                json_build_object('id', o.id, 'position', o.position, 'text', o.text)
                ORDER BY o.position
            )
            FROM poll_options o
            WHERE o.poll_id = p.id
        ),
        '[]'::json
    ) AS options`

// Storage — репозиторий опросов.
type Storage struct {
	db *pgstorage.DB
}

// New создаёт репозиторий опросов.
func New(db *pgstorage.DB) *Storage {
	return &Storage{db: db}
}

// Create сохраняет опрос вместе с вариантами одной транзакцией.
func (s *Storage) Create(ctx context.Context, poll dto.Poll) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insertPoll = `
        INSERT INTO polls (id, title, question, kind, status, max_choices, opens_at, closes_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8);`

	if _, err := tx.Exec(ctx, insertPoll,
		poll.ID, poll.Title, poll.Question, string(poll.Kind), string(poll.Status),
		poll.MaxChoices, poll.OpensAt, poll.ClosesAt,
	); err != nil {
		return fmt.Errorf("insert poll: %w", err)
	}

	const insertOption = `
        INSERT INTO poll_options (id, poll_id, position, text)
        VALUES ($1, $2, $3, $4);`

	batch := &pgx.Batch{}
	for _, option := range poll.Options {
		batch.Queue(insertOption, option.ID, poll.ID, option.Position, option.Text)
	}

	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("insert poll options: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// GetByID возвращает опрос или dto.ErrNotFound.
func (s *Storage) GetByID(ctx context.Context, pollID uuid.UUID) (dto.Poll, error) {
	query := `SELECT` + selectColumns + ` FROM polls p WHERE p.id = $1;`

	var row pollRow
	if err := s.db.QueryRow(ctx, query, pollID).Scan(row.scanTargets()...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dto.Poll{}, dto.ErrNotFound
		}

		return dto.Poll{}, fmt.Errorf("select poll: %w", err)
	}

	return rowToPoll(row)
}

// List возвращает опросы для админки, свежие сверху.
func (s *Storage) List(ctx context.Context, limit, offset int) ([]dto.Poll, error) {
	query := `SELECT` + selectColumns + `
        FROM polls p
        ORDER BY p.created_at DESC
        LIMIT $1 OFFSET $2;`

	rows, err := s.db.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("select polls: %w", err)
	}
	defer rows.Close()

	polls := make([]dto.Poll, 0, limit)
	for rows.Next() {
		var row pollRow
		if err := rows.Scan(row.scanTargets()...); err != nil {
			return nil, fmt.Errorf("scan poll: %w", err)
		}

		poll, err := rowToPoll(row)
		if err != nil {
			return nil, err
		}

		polls = append(polls, poll)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate polls: %w", err)
	}

	return polls, nil
}

// ListForSnapshot возвращает опросы, по которым снапшоттеру есть что сохранять:
// активные и те, что закрылись не позже closedGrace назад.
//
// Недавно закрытые нужны, чтобы финальные цифры точно доехали до PostgreSQL
// после того, как голосование прекратилось.
func (s *Storage) ListForSnapshot(ctx context.Context, closedGrace time.Duration) ([]uuid.UUID, error) {
	const query = `
        SELECT id
        FROM polls
        WHERE status = 'active'
           OR (status = 'closed' AND updated_at > now() - make_interval(secs => $1));`

	rows, err := s.db.Query(ctx, query, closedGrace.Seconds())
	if err != nil {
		return nil, fmt.Errorf("select polls for snapshot: %w", err)
	}
	defer rows.Close()

	var pollIDs []uuid.UUID
	for rows.Next() {
		var pollID uuid.UUID
		if err := rows.Scan(&pollID); err != nil {
			return nil, fmt.Errorf("scan poll id: %w", err)
		}

		pollIDs = append(pollIDs, pollID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate polls for snapshot: %w", err)
	}

	return pollIDs, nil
}

// SetStatus переводит опрос в новое состояние.
func (s *Storage) SetStatus(ctx context.Context, pollID uuid.UUID, status dto.Status) error {
	const query = `
        UPDATE polls
        SET status = $2, updated_at = now()
        WHERE id = $1;`

	tag, err := s.db.Exec(ctx, query, pollID, string(status))
	if err != nil {
		return fmt.Errorf("update poll status: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return dto.ErrNotFound
	}

	return nil
}

func rowToPoll(row pollRow) (dto.Poll, error) {
	var options []optionRow
	if err := json.Unmarshal(row.options, &options); err != nil {
		return dto.Poll{}, fmt.Errorf("decode poll options: %w", err)
	}

	return row.toDomain(options), nil
}
