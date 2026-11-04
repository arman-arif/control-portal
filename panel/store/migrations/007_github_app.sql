-- At most one GitHub App per account, made through GitHub's manifest flow.
CREATE TABLE github_apps (
    user_id        BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    app_id         BIGINT NOT NULL UNIQUE,
    slug           TEXT NOT NULL,
    name           TEXT NOT NULL,
    html_url       TEXT NOT NULL,
    private_key    BYTEA NOT NULL, -- sealed with KEEL_SECRET_KEY
    webhook_secret BYTEA NOT NULL, -- sealed with KEEL_SECRET_KEY
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Set when a site deploys through the GitHub App instead of a deploy key.
ALTER TABLE sites ADD COLUMN github_installation_id BIGINT;
