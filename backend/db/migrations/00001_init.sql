-- +goose Up

CREATE TYPE poll_kind AS ENUM ('single_choice', 'multiple_choice', 'ab');
CREATE TYPE poll_status AS ENUM ('draft', 'active', 'closed');

CREATE TABLE polls (
    id          uuid        PRIMARY KEY,
    title       text        NOT NULL,
    question    text        NOT NULL,
    kind        poll_kind   NOT NULL,
    status      poll_status NOT NULL DEFAULT 'draft',
    -- Для multiple_choice — сколько вариантов зритель может отметить.
    max_choices smallint    NOT NULL DEFAULT 1 CHECK (max_choices >= 1),
    opens_at    timestamptz,
    closes_at   timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX polls_status_created_at_idx ON polls (status, created_at DESC);

CREATE TABLE poll_options (
    id       uuid     PRIMARY KEY,
    poll_id  uuid     NOT NULL REFERENCES polls (id) ON DELETE CASCADE,
    position smallint NOT NULL,
    text     text     NOT NULL,
    UNIQUE (poll_id, position)
);

CREATE INDEX poll_options_poll_id_idx ON poll_options (poll_id);

-- Агрегаты, перенесённые снапшоттером из Redis. Отдельной строки на голос нет
-- и быть не может: 20М голосов за минуту в реляционную БД не пишутся.
CREATE TABLE poll_results (
    poll_id    uuid        NOT NULL REFERENCES polls (id) ON DELETE CASCADE,
    option_id  uuid        NOT NULL REFERENCES poll_options (id) ON DELETE CASCADE,
    votes      bigint      NOT NULL DEFAULT 0 CHECK (votes >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (poll_id, option_id)
);

-- voters отдельно от суммы по вариантам: в multiple_choice один человек даёт
-- несколько отметок, а проценты считаются от людей.
CREATE TABLE poll_totals (
    poll_id    uuid        PRIMARY KEY REFERENCES polls (id) ON DELETE CASCADE,
    voters     bigint      NOT NULL DEFAULT 0 CHECK (voters >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- История агрегатов: даёт админке динамику голосования по ходу ролика.
CREATE TABLE result_snapshots (
    id          bigserial   PRIMARY KEY,
    poll_id     uuid        NOT NULL REFERENCES polls (id) ON DELETE CASCADE,
    captured_at timestamptz NOT NULL DEFAULT now(),
    voters      bigint      NOT NULL,
    counts      jsonb       NOT NULL
);

CREATE INDEX result_snapshots_poll_captured_idx
    ON result_snapshots (poll_id, captured_at DESC);

-- +goose Down

DROP TABLE result_snapshots;
DROP TABLE poll_totals;
DROP TABLE poll_results;
DROP TABLE poll_options;
DROP TABLE polls;
DROP TYPE poll_status;
DROP TYPE poll_kind;
