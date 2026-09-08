// Package counters копит голоса в памяти процесса и отдаёт накопленное
// наружу порциями.
//
// Смысл пакета в том, чтобы разорвать связь «один голос — одна запись во
// внешнее хранилище». На пике опроса реплика принимает десятки тысяч голосов
// в секунду, но во внешний счётчик уходит всего несколько операций в секунду:
// ровно столько, сколько раз сработал сброс.
package counters

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// cacheLineSize — типичная длина кэш-линии на amd64/arm64.
const cacheLineSize = 64

// stripe — атомарный счётчик, занимающий кэш-линию целиком.
//
// Без padding'а несколько счётчиков ложатся в одну линию, и ядра начинают
// перегонять её друг у друга при каждом инкременте (false sharing). На
// сотнях тысяч инкрементов в секунду это заметно дороже самой операции.
type stripe struct {
	value atomic.Int64
	_     [cacheLineSize - 8]byte
}

// striped — один логический счётчик, разложенный по нескольким кэш-линиям.
type striped struct {
	cells []stripe
}

func newStriped(stripes int) *striped {
	return &striped{cells: make([]stripe, stripes)}
}

func (s *striped) add(delta int64) {
	// rand.Uint32 из math/rand/v2 берёт состояние из текущего P и не блокируется,
	// поэтому как источник равномерного индекса он практически бесплатен.
	//nolint:gosec // Выбор кэш-линии, а не секрет: предсказуемость индекса безвредна.
	idx := int(rand.Uint32()) % len(s.cells)
	s.cells[idx].value.Add(delta)
}

// drain забирает накопленное и обнуляет счётчик.
func (s *striped) drain() int64 {
	var total int64
	for i := range s.cells {
		total += s.cells[i].value.Swap(0)
	}

	return total
}

// pollCounters — счётчики одного опроса.
type pollCounters struct {
	stripes int
	voters  *striped
	// options неизменяема после публикации: замена идёт через atomic-подмену
	// всей карты, поэтому горячий путь читает её без единой блокировки.
	options atomic.Pointer[map[uuid.UUID]*striped]
	mu      sync.Mutex
}

// Delta — прирост по одному опросу за интервал сброса.
type Delta struct {
	PollID  uuid.UUID
	Voters  int64
	Options map[uuid.UUID]int64
}

// Total — сколько голосов описывает дельта (для метрик).
func (d Delta) Total() int64 {
	var total int64
	for _, votes := range d.Options {
		total += votes
	}

	return total
}

// Aggregator копит голоса в памяти до ближайшего сброса.
//
// Он безопасен для конкурентного использования и на горячем пути не берёт
// ни одной блокировки: карты опросов и вариантов подменяются целиком, а сами
// счётчики атомарные.
type Aggregator struct {
	stripes int
	polls   atomic.Pointer[map[uuid.UUID]*pollCounters]
	mu      sync.Mutex
}

// NewAggregator создаёт агрегатор, где каждый счётчик разложен на stripes ячеек.
func NewAggregator(stripes int) *Aggregator {
	if stripes < 1 {
		stripes = 1
	}

	a := &Aggregator{stripes: stripes}
	empty := make(map[uuid.UUID]*pollCounters)
	a.polls.Store(&empty)

	return a
}

// AddVote учитывает один голос: +1 каждому выбранному варианту и +1 к числу
// проголосовавших.
func (a *Aggregator) AddVote(pollID uuid.UUID, optionIDs []uuid.UUID) {
	counters := a.pollCounters(pollID)
	counters.voters.add(1)

	for _, optionID := range optionIDs {
		counters.option(optionID).add(1)
	}
}

// Restore возвращает дельты обратно в агрегатор. Нужен, когда сброс во внешнее
// хранилище не удался: голоса подождут в памяти до следующей попытки, вместо
// того чтобы пропасть.
func (a *Aggregator) Restore(deltas []Delta) {
	for _, delta := range deltas {
		counters := a.pollCounters(delta.PollID)
		counters.voters.add(delta.Voters)

		for optionID, votes := range delta.Options {
			counters.option(optionID).add(votes)
		}
	}
}

// Drain забирает всё накопленное и обнуляет счётчики. Опросы без движения
// в результат не попадают.
func (a *Aggregator) Drain() []Delta {
	polls := *a.polls.Load()
	deltas := make([]Delta, 0, len(polls))

	for pollID, counters := range polls {
		options := make(map[uuid.UUID]int64)
		for optionID, counter := range *counters.options.Load() {
			if votes := counter.drain(); votes != 0 {
				options[optionID] = votes
			}
		}

		voters := counters.voters.drain()
		if voters == 0 && len(options) == 0 {
			continue
		}

		deltas = append(deltas, Delta{PollID: pollID, Voters: voters, Options: options})
	}

	return deltas
}

func (a *Aggregator) pollCounters(pollID uuid.UUID) *pollCounters {
	if counters, ok := (*a.polls.Load())[pollID]; ok {
		return counters
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Пока ждали блокировку, счётчики мог создать кто-то ещё.
	current := *a.polls.Load()
	if counters, ok := current[pollID]; ok {
		return counters
	}

	counters := &pollCounters{stripes: a.stripes, voters: newStriped(a.stripes)}
	options := make(map[uuid.UUID]*striped)
	counters.options.Store(&options)

	next := make(map[uuid.UUID]*pollCounters, len(current)+1)
	for id, existing := range current {
		next[id] = existing
	}
	next[pollID] = counters
	a.polls.Store(&next)

	return counters
}

func (p *pollCounters) option(optionID uuid.UUID) *striped {
	if counter, ok := (*p.options.Load())[optionID]; ok {
		return counter
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	current := *p.options.Load()
	if counter, ok := current[optionID]; ok {
		return counter
	}

	counter := newStriped(p.stripes)
	next := make(map[uuid.UUID]*striped, len(current)+1)
	for id, existing := range current {
		next[id] = existing
	}
	next[optionID] = counter
	p.options.Store(&next)

	return counter
}
