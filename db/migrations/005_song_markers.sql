-- Sections of a song a user saved while practicing (a chorus, a hard bridge),
-- to loop again later. Each user has their own.
CREATE TABLE IF NOT EXISTS song_markers (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    song_id    uuid NOT NULL REFERENCES songs (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    label      text NOT NULL,
    start_s    double precision NOT NULL,
    end_s      double precision NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (start_s >= 0 AND end_s > start_s)
);

CREATE INDEX IF NOT EXISTS song_markers_song_user_idx ON song_markers (song_id, user_id);
