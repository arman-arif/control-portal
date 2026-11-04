CREATE TABLE databases (
    id          BIGSERIAL PRIMARY KEY,
    server_id   BIGINT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'creating' CHECK (status IN ('creating', 'deleting', 'active', 'failed')),
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT databases_server_name_key UNIQUE (server_id, name)
);

CREATE TABLE database_users (
    id          BIGSERIAL PRIMARY KEY,
    server_id   BIGINT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    password    BYTEA NOT NULL, -- sealed with KEEL_SECRET_KEY (panel/secrets)
    status      TEXT NOT NULL DEFAULT 'creating' CHECK (status IN ('creating', 'updating', 'deleting', 'active', 'failed')),
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT database_users_server_name_key UNIQUE (server_id, name)
);

-- The databases each user may use, as the panel last asked the server to set.
CREATE TABLE database_grants (
    database_id      BIGINT NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    database_user_id BIGINT NOT NULL REFERENCES database_users(id) ON DELETE CASCADE,
    PRIMARY KEY (database_id, database_user_id)
);

ALTER TABLE jobs ADD COLUMN database_id BIGINT REFERENCES databases(id) ON DELETE SET NULL;
ALTER TABLE jobs ADD COLUMN database_user_id BIGINT REFERENCES database_users(id) ON DELETE SET NULL;
