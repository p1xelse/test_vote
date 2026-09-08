package counters

import "github.com/google/uuid"

// Snapshot — счётчики опроса, собранные из внешнего хранилища.
//
// Тип живёт здесь, а не рядом с Redis-хранилищем, чтобы слой результатов не
// зависел от того, где именно лежат счётчики.
type Snapshot struct {
	Voters  int64
	Options map[uuid.UUID]int64
	// Found=false означает, что счётчиков по опросу во внешнем хранилище нет:
	// либо по нему ещё не голосовали, либо ключи уже истекли.
	Found bool
}
