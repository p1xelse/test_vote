// Сценарий для потолка одной ручки: только POST /vote, без похода за страницей
// опроса.
//
// Все запросы идут без куки, поэтому зритель опознаётся по подсети и почти все
// голоса отбиваются как повторные. Для замера это не помеха: путь внутри
// сервиса тот же самый — разбор запроса, проверка опроса из кэша и один
// round-trip в Redis. Меняется только последняя строчка ответа.
import http from 'k6/http';
import { Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://host.docker.internal:8080';
const POLL_ID = __ENV.POLL_ID;
const OPTION_ID = __ENV.OPTION_ID;

const voteDuration = new Trend('vote_duration', true);

export const options = {
  scenarios: {
    ceiling: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 200),
      duration: __ENV.DURATION || '30s',
    },
  },
  thresholds: {
    'vote_duration': ['p(99)<500'],
  },
};

export default function () {
  if (!POLL_ID || !OPTION_ID) {
    throw new Error('нужны POLL_ID и OPTION_ID, см. loadtest/README.md');
  }

  const response = http.post(
    `${BASE_URL}/api/v1/polls/${POLL_ID}/vote`,
    JSON.stringify({ option_ids: [OPTION_ID] }),
    { headers: { 'Content-Type': 'application/json' } },
  );

  voteDuration.add(response.timings.duration);
}
