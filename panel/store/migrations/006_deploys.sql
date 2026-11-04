-- Structured output of a finished job, such as the commit a deploy checked out.
ALTER TABLE jobs ADD COLUMN result JSONB;

ALTER TABLE sites ADD COLUMN repo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE sites ADD COLUMN branch TEXT NOT NULL DEFAULT 'main';
ALTER TABLE sites ADD COLUMN deploy_script TEXT; -- NULL means Keel's default script
ALTER TABLE sites ADD COLUMN deploy_timeout INT NOT NULL DEFAULT 60 CHECK (deploy_timeout BETWEEN 1 AND 180);
ALTER TABLE sites ADD COLUMN deploy_key TEXT NOT NULL DEFAULT ''; -- public half; the private key stays on the server
ALTER TABLE sites ADD COLUMN webhook_secret BYTEA; -- sealed with KEEL_SECRET_KEY

CREATE TABLE deployments (
    id             BIGSERIAL PRIMARY KEY,
    site_id        BIGINT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    job_id         BIGINT REFERENCES jobs(id) ON DELETE SET NULL,
    trigger        TEXT NOT NULL CHECK (trigger IN ('manual', 'push')),
    commit_sha     TEXT NOT NULL DEFAULT '',
    commit_message TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX deployments_site_id_idx ON deployments(site_id, id DESC);
