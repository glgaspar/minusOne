CREATE TABLE IF NOT EXISTS songs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_url   text NOT NULL,
    title        text,
    author       text,

    -- object key of the downloaded original in the bucket, e.g. songs/<id>/original.m4a
    original_key text,
    extension    text,

    -- job queue state: pending -> processing -> done | failed
    status       text NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'processing', 'done', 'failed')),
    attempts     int  NOT NULL DEFAULT 0,
    last_error   text,
    locked_at    timestamptz,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- the worker only ever scans for claimable rows, so keep the index small
CREATE INDEX IF NOT EXISTS songs_claimable_idx
    ON songs (created_at)
    WHERE status IN ('pending', 'processing');

CREATE TABLE IF NOT EXISTS tracks (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    song_id     uuid NOT NULL REFERENCES songs (id) ON DELETE CASCADE,
    type        text NOT NULL, -- vocals, drums, bass, other, instrumental...
    storage_key text NOT NULL, -- e.g. songs/<song_id>/vocals.wav
    extension   text NOT NULL,
    size_bytes  bigint,
    created_at  timestamptz NOT NULL DEFAULT now(),

    UNIQUE (song_id, type)
);
