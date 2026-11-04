-- ssl turns true once the site's first certificate is issued; from then on
-- the agent keeps a certificate covering the site's names.
ALTER TABLE sites ADD COLUMN ssl BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE sites ADD COLUMN acme_email TEXT NOT NULL DEFAULT '';
