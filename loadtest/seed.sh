#!/usr/bin/env bash
# Создаёт демо-опрос, открывает голосование и печатает ссылку для QR-кода.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
ADMIN_TOKEN="${ADMIN_TOKEN:-local-admin-token}"

poll=$(curl -sS -X POST "$BASE_URL/api/v1/admin/polls" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
        "title": "Прайм-тайм, 21:00",
        "question": "Кто должен победить?",
        "kind": "single_choice",
        "options": ["Алиса", "Борис", "Виктор", "Галина"]
      }')

# Первый "id" в ответе — это id опроса, дальше идут id вариантов.
poll_id=$(printf '%s' "$poll" | grep -o '"id":"[0-9a-f-]\{36\}"' | head -1 | cut -d'"' -f4)

if [ -z "$poll_id" ]; then
  echo "не удалось создать опрос: $poll" >&2
  exit 1
fi

curl -sS -o /dev/null -X POST "$BASE_URL/api/v1/admin/polls/$poll_id/status" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"status":"active"}'

echo "poll_id:    $poll_id"
echo "голосовать: $BASE_URL/?poll=$poll_id"
echo "админка:    $BASE_URL/admin.html"
