package storage

import (
	"strconv"

	"github.com/google/uuid"
)

// votersField — поле хэша, где лежит число проголосовавших.
//
// Оно живёт рядом со счётчиками вариантов, чтобы читаться тем же HGETALL.
// Двоеточия в имени делают его несовместимым с любым UUID, так что перепутать
// его с вариантом ответа невозможно.
const votersField = "::voters::"

// counterKey — ключ шарда счётчиков опроса.
//
// Номер шарда стоит в конце ключа и участвует в вычислении слота: счётчики
// одного опроса расходятся по разным нодам Redis Cluster, и опрос не упирается
// в пропускную способность одной из них.
func counterKey(pollID uuid.UUID, shard int) string {
	return "vote:c:" + pollID.String() + ":" + strconv.Itoa(shard)
}
