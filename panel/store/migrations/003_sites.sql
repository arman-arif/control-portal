CREATE TABLE sites (
    id          BIGSERIAL PRIMARY KEY,
    server_id   BIGINT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    domain      TEXT NOT NULL,
    aliases     TEXT[] NOT NULL DEFAULT '{}',
    unix_user   TEXT NOT NULL, -- the site's system user; system_user is reserved in SQL
    web_dir     TEXT NOT NULL DEFAULT 'public',
    php_version TEXT NOT NULL,
    -- What the last site job did to the server: creating, updating and
    -- deleting while one is queued or running, then active or failed.
    status      TEXT NOT NULL DEFAULT 'creating'
                CHECK (status IN ('creating', 'updating', 'deleting', 'active', 'failed')),
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT sites_server_domain_key UNIQUE (server_id, domain),
    CONSTRAINT sites_server_unix_user_key UNIQUE (server_id, unix_user)
);
CREATE INDEX sites_user_id_idx ON sites(user_id);

ALTER TABLE jobs ADD COLUMN site_id BIGINT REFERENCES sites(id) ON DELETE SET NULL;
CREATE INDEX jobs_site_id_idx ON jobs(site_id, id DESC) WHERE site_id IS NOT NULL;
