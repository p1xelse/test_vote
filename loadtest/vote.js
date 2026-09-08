// Нагрузочный сценарий: минута эфира, за которую зрители успевают
// отсканировать QR-код и проголосовать.
//
// Каждая итерация — отдельный зритель: своя банка кук, свой поход за страницей
// опроса и один голос. Это ровно тот путь, который проходит живой человек,
// поэтому в цифрах видна стоимость всего сценария, а не одной ручки.
import http from 'k6/http';
import { check, fail } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://host.docker.internal:8080';
const ADMIN_TOKEN = __ENV.ADMIN_TOKEN || 'local-admin-token';

// Пик и длительность подбираются под машину: локально 300k RPS не сгенерировать,
// смысл замера — найти потолок одного инстанса и посчитать от него.
const PEAK_RPS = Number(__ENV.PEAK_RPS || 3000);
const RAMP = __ENV.RAMP || '15s';
const HOLD = __ENV.HOLD || '45s';

const voteDuration = new Trend('vote_duration', true);
const pollDuration = new Trend('poll_fetch_duration', true);
const acceptedRate = new Rate('votes_accepted');
const duplicates = new Counter('votes_duplicate');
const rejected = new Counter('votes_rejected');

export const options = {
  scenarios: {
    tv_spike: {
      executor: 'ramping-arrival-rate',
      timeUnit: '1s',
      startRate: 0,
      preAllocatedVUs: Number(__ENV.VUS || 400),
      maxVUs: Number(__ENV.MAX_VUS || 4000),
      stages: [
        // Всплеск в первые секунды после появления QR на экране.
        { target: PEAK_RPS, duration: RAMP },
        { target: PEAK_RPS, duration: HOLD },
      ],
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.01'],
    'vote_duration': ['p(95)<200', 'p(99)<500'],
    'votes_accepted': ['rate>0.99'],
  },
};

function adminParams() {
  return {
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${ADMIN_TOKEN}` },
  };
}

// setup создаёт опрос и открывает голосование, чтобы тест был самодостаточным.
export function setup() {
  const created = http.post(
    `${BASE_URL}/api/v1/admin/polls`,
    JSON.stringify({
      title: 'Нагрузочный эфир',
      question: 'Кто должен победить?',
      kind: 'single_choice',
      options: ['Алиса', 'Борис', 'Виктор', 'Галина'],
    }),
    adminParams(),
  );

  if (created.status !== 201) {
    fail(`не удалось создать опрос: ${created.status} ${created.body}`);
  }

  const poll = created.json();
  const activated = http.post(
    `${BASE_URL}/api/v1/admin/polls/${poll.id}/status`,
    JSON.stringify({ status: 'active' }),
    adminParams(),
  );

  if (activated.status !== 200) {
    fail(`не удалось открыть опрос: ${activated.status} ${activated.body}`);
  }

  console.log(`опрос ${poll.id}: ${BASE_URL}/?poll=${poll.id}`);

  return { pollId: poll.id, optionIds: poll.options.map(o => o.id) };
}

export default function (data) {
  // Своя банка кук на итерацию — это и есть «новый зритель». Без неё все
  // виртуальные пользователи слились бы в одного и упёрлись в дедупликацию.
  const jar = new http.CookieJar();
  const params = { jar, headers: { 'Content-Type': 'application/json' } };

  const poll = http.get(`${BASE_URL}/api/v1/polls/${data.pollId}`, params);
  pollDuration.add(poll.timings.duration);
  check(poll, { 'опрос отдался': r => r.status === 200 });

  const optionId = data.optionIds[Math.floor(Math.random() * data.optionIds.length)];
  const vote = http.post(
    `${BASE_URL}/api/v1/polls/${data.pollId}/vote`,
    JSON.stringify({ option_ids: [optionId] }),
    params,
  );

  voteDuration.add(vote.timings.duration);
  acceptedRate.add(vote.status === 202);

  if (vote.status === 409) {
    duplicates.add(1);
  } else if (vote.status !== 202) {
    rejected.add(1);
  }
}

// teardown закрывает опрос и печатает итог: заодно проверяет, что счётчики
// сошлись с числом принятых голосов.
export function teardown(data) {
  http.post(
    `${BASE_URL}/api/v1/admin/polls/${data.pollId}/status`,
    JSON.stringify({ status: 'closed' }),
    adminParams(),
  );

  const results = http.get(`${BASE_URL}/api/v1/polls/${data.pollId}/results`);
  if (results.status === 200) {
    const body = results.json();
    console.log(`итог: проголосовало ${body.voters}`);
    body.options.forEach(o => console.log(`  ${o.text}: ${o.votes} (${(o.share * 100).toFixed(1)}%)`));
  }
}
