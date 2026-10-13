CREATE TABLE IF NOT EXISTS users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      text NOT NULL,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- "Gustavo" and "gustavo" are the same account
CREATE UNIQUE INDEX IF NOT EXISTS users_username_idx ON users (lower(username));

CREATE TABLE IF NOT EXISTS sessions (
    -- sha256 of the cookie token, so a leaked db dump can't be used to log in
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id);

ALTER TABLE songs ADD COLUMN IF NOT EXISTS added_by uuid REFERENCES users (id) ON DELETE SET NULL;

-- source_url is stored canonicalized, so the same video can't be added twice
CREATE UNIQUE INDEX IF NOT EXISTS songs_source_url_idx ON songs (source_url);
