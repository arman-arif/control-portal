CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    token_hash BYTEA PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions(user_id);

CREATE TABLE servers (
    id                       BIGSERIAL PRIMARY KEY,
    user_id                  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                     TEXT NOT NULL,
    hostname                 TEXT NOT NULL DEFAULT '',
    os                       TEXT NOT NULL DEFAULT '',
    kernel                   TEXT NOT NULL DEFAULT '',
    arch                     TEXT NOT NULL DEFAULT '',
    cpus                     INTEGER NOT NULL DEFAULT 0,
    agent_version            TEXT NOT NULL DEFAULT '',
    ip                       TEXT NOT NULL DEFAULT '',
    install_token_hash       BYTEA UNIQUE,
    install_token_expires_at TIMESTAMPTZ,
    agent_secret_hash        BYTEA UNIQUE,
    linked_at                TIMESTAMPTZ,
    last_seen_at             TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX servers_user_id_idx ON servers(user_id);
