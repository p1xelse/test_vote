// Package storage хранит агрегаты голосования в PostgreSQL: текущий итог по
// каждому варианту и историю снимков для динамики.
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
	"github.com/dneumoin/test_vote/backend/internal/service/results/dto"
)

// Storage — репозиторий результатов.
type Storage struct {
	db *pgstorage.DB
}

// New создаёт репозиторий результатов.
func New(db *pgstorage.DB) *Storage {
	return &Storage{db: db}
}

// Save записывает текущее состояние счётчиков и добавляет точку в историю.
//
// Вызывается снапшоттером раз в несколько секунд, а не на каждый голос:
// на горячем пути этот код не участвует.
func (s *Storage) Save(
	ctx context.Context,
	pollID uuid.UUID,
	voters int64,
	counts map[uuid.UUID]int64,
) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const upsertTotals = `
        INSERT INTO poll_totals (poll_id, voters, updated_at)
        VALUES ($1, $2, now())
        ON CONFLICT (poll_id) DO UPDATE
        SET voters = EXCLUDED.voters, updated_at = EXCLUDED.updated_at;`

	if _, err := tx.Exec(ctx, upsertTotals, pollID, voters); err != nil {
		return fmt.Errorf("upsert poll totals: %w", err)
	}

	const upsertResult = `
        INSERT INTO poll_results (poll_id, option_id, votes, updated_at)
        VALUES ($1, $2, $3, now())
        ON CONFLICT (poll_id, option_id) DO UPDATE
        SET votes = EXCLUDED.votes, updated_at = EXCLUDED.updated_at;`

	batch := &pgx.Batch{}
	for optionID, votes := range counts {
		batch.Queue(upsertResult, pollID, optionID, votes)
	}

	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("upsert poll results: %w", err)
		}
	}

	payload, err := json.Marshal(countsToJSON(counts))
	if err != nil {
		return fmt.Errorf("encode counts: %w", err)
	}

	const insertSnapshot = `
        INSERT INTO result_snapshots (poll_id, voters, counts)
        VALUES ($1, $2, $3);`

	if _, err := tx.Exec(ctx, insertSnapshot, pollID, voters, payload); err != nil {
		return fmt.Errorf("insert result snapshot: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// Load возвращает сохранённый итог опроса: число проголосовавших и голоса по
// вариантам.
func (s *Storage) Load(
	ctx context.Context,
	pollID uuid.UUID,
) (voters int64, counts map[uuid.UUID]int64, err error) {
	const totalsQuery = `SELECT voters FROM poll_totals WHERE poll_id = $1;`

	// Отсутствие строки — нормальная ситуация: по опросу ещё не было ни одного
	// снапшота, значит и голосов ноль.
	if err := s.db.QueryRow(ctx, totalsQuery, pollID).Scan(&voters); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, fmt.Errorf("select poll totals: %w", err)
	}

	const resultsQuery = `SELECT option_id, votes FROM poll_results WHERE poll_id = $1;`

	rows, err := s.db.Query(ctx, resultsQuery, pollID)
	if err != nil {
		return 0, nil, fmt.Errorf("select poll results: %w", err)
	}
	defer rows.Close()

	counts = make(map[uuid.UUID]int64)
	for rows.Next() {
		var (
			optionID uuid.UUID
			votes    int64
		)

		if err := rows.Scan(&optionID, &votes); err != nil {
			return 0, nil, fmt.Errorf("scan poll result: %w", err)
		}

		counts[optionID] = votes
	}

	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("iterate poll results: %w", err)
	}

	return voters, counts, nil
}

// Timeline возвращает историю снимков, свежие сверху.
func (s *Storage) Timeline(ctx context.Context, pollID uuid.UUID, limit int) ([]dto.TimelinePoint, error) {
	const query = `
        SELECT captured_at, voters, counts
        FROM result_snapshots
        WHERE poll_id = $1
        ORDER BY captured_at DESC
        LIMIT $2;`

	rows, err := s.db.Query(ctx, query, pollID, limit)
	if err != nil {
		return nil, fmt.Errorf("select result snapshots: %w", err)
	}
	defer rows.Close()

	points := make([]dto.TimelinePoint, 0, limit)
	for rows.Next() {
		var (
			capturedAt time.Time
			voters     int64
			payload    []byte
		)

		if err := rows.Scan(&capturedAt, &voters, &payload); err != nil {
			return nil, fmt.Errorf("scan result snapshot: %w", err)
		}

		raw := map[string]int64{}
		if err := json.Unmarshal(payload, &raw); err != nil {
			return nil, fmt.Errorf("decode counts: %w", err)
		}

		counts := make(map[uuid.UUID]int64, len(raw))
		for id, votes := range raw {
			optionID, err := uuid.Parse(id)
			if err != nil {
				return nil, fmt.Errorf("parse option id %q: %w", id, err)
			}

			counts[optionID] = votes
		}

		points = append(points, dto.TimelinePoint{CapturedAt: capturedAt, Voters: voters, Counts: counts})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate result snapshots: %w", err)
	}

	return points, nil
}

func countsToJSON(counts map[uuid.UUID]int64) map[string]int64 {
	out := make(map[string]int64, len(counts))
	for optionID, votes := range counts {
		out[optionID.String()] = votes
	}

	return out
}
