// Package storage — горячий путь голосования в Redis: отметки «уже голосовал»
// и агрегированные счётчики опроса.
//
// В PostgreSQL этот пакет не заглядывает: за минуту опроса сюда приходят
// миллионы операций, и реляционная база такого темпа не выдержит.
package storage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/rueidis"

	"github.com/dneumoin/test_vote/backend/internal/counters"
)

// Storage работает со счётчиками и дедуп-ключами в Redis.
type Storage struct {
	client rueidis.Client
	shards int
	// writeShard — шард, в который эта реплика сбрасывает свои дельты.
	// Реплика пишет всегда в один ключ, поэтому сброс — это одна операция
	// HINCRBY на вариант, а не размазывание каждого голоса по кластеру.
	writeShard int
}

// Options — настройки шардирования счётчиков.
type Options struct {
	// Shards — сколько ключей приходится на счётчики одного опроса.
	Shards int
	// InstanceID определяет, какой из шардов достанется этой реплике.
	InstanceID string
}

// New создаёт хранилище голосов.
func New(client rueidis.Client, opts Options) *Storage {
	shards := opts.Shards
	if shards < 1 {
		shards = 1
	}

	return &Storage{
		client:     client,
		shards:     shards,
		writeShard: shardForInstance(opts.InstanceID, shards),
	}
}

// MarkVoted ставит отметку о голосовании и сообщает, была ли она первой.
//
// SET NX EX — одна атомарная команда и один round-trip на голос. Скрипт на Lua
// здесь ничего не добавил бы: проверка и установка и так неразделимы.
func (s *Storage) MarkVoted(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	cmd := s.client.B().Set().Key(key).Value("1").Nx().Ex(ttl).Build()

	err := s.client.Do(ctx, cmd).Error()
	switch {
	case err == nil:
		return true, nil
	case rueidis.IsRedisNil(err):
		// NX не сработал — ключ уже был, значит зритель уже голосовал.
		return false, nil
	default:
		return false, fmt.Errorf("mark voted: %w", err)
	}
}

// FlushDeltas добавляет накопленные в памяти дельты к счётчикам в Redis.
//
// Реализует counters.Sink. Операции идут одним пакетом: rueidis склеивает их
// в общий пайплайн, поэтому сброс стоит примерно один round-trip независимо
// от числа опросов и вариантов.
func (s *Storage) FlushDeltas(ctx context.Context, deltas []counters.Delta) error {
	if len(deltas) == 0 {
		return nil
	}

	cmds := make(rueidis.Commands, 0, len(deltas)*4)
	for _, delta := range deltas {
		key := counterKey(delta.PollID, s.writeShard)

		if delta.Voters != 0 {
			cmds = append(cmds, s.client.B().Hincrby().
				Key(key).Field(votersField).Increment(delta.Voters).Build())
		}

		for optionID, votes := range delta.Options {
			cmds = append(cmds, s.client.B().Hincrby().
				Key(key).Field(optionID.String()).Increment(votes).Build())
		}

		// Счётчики закрытого опроса не должны занимать память вечно. TTL
		// продлевается на каждом сбросе, пока опрос жив, и истекает через сутки
		// после последнего голоса — к этому моменту итог давно лежит в PostgreSQL.
		cmds = append(cmds, s.client.B().Expire().Key(key).Seconds(int64(counterTTL.Seconds())).Build())
	}

	for _, result := range s.client.DoMulti(ctx, cmds...) {
		if err := result.Error(); err != nil {
			return fmt.Errorf("flush counters: %w", err)
		}
	}

	return nil
}

// ReadCounts складывает счётчики опроса со всех шардов.
//
// Читается это редко: результаты кэшируются в памяти сервиса, поэтому даже
// на пике сюда приходит пара запросов в секунду с реплики.
func (s *Storage) ReadCounts(ctx context.Context, pollID uuid.UUID) (counters.Snapshot, error) {
	cmds := make(rueidis.Commands, 0, s.shards)
	for shard := range s.shards {
		cmds = append(cmds, s.client.B().Hgetall().Key(counterKey(pollID, shard)).Build())
	}

	snapshot := counters.Snapshot{Options: make(map[uuid.UUID]int64)}

	for _, result := range s.client.DoMulti(ctx, cmds...) {
		entries, err := result.AsStrMap()
		if err != nil {
			if rueidis.IsRedisNil(err) {
				continue
			}

			return counters.Snapshot{}, fmt.Errorf("read counters: %w", err)
		}

		if len(entries) > 0 {
			snapshot.Found = true
		}

		for field, raw := range entries {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return counters.Snapshot{}, fmt.Errorf("parse counter %q: %w", field, err)
			}

			if field == votersField {
				snapshot.Voters += value

				continue
			}

			optionID, err := uuid.Parse(field)
			if err != nil {
				return counters.Snapshot{}, fmt.Errorf("parse option id %q: %w", field, err)
			}

			snapshot.Options[optionID] += value
		}
	}

	return snapshot, nil
}

// counterTTL — сколько счётчики опроса живут в Redis после последнего голоса.
const counterTTL = 24 * time.Hour

// shardForInstance закрепляет за репликой один шард счётчиков.
func shardForInstance(instanceID string, shards int) int {
	if instanceID == "" || shards < 1 {
		return 0
	}

	// FNV-1a: достаточно равномерен, чтобы реплики разошлись по разным шардам,
	// и не тянет за собой криптографию там, где она не нужна.
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)

	hash := uint64(offset64)
	for i := range len(instanceID) {
		hash ^= uint64(instanceID[i])
		hash *= prime64
	}

	//nolint:gosec // shards >= 1 проверено выше, переполнения при конверсии нет.
	return int(hash % uint64(shards))
}
