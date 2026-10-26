-- What the processor learns about a song beyond its audio: the lyrics, timed,
-- and its chords. JSON, in the shapes of db.Lyrics and db.Chords.
CREATE TABLE IF NOT EXISTS song_annotations (
    song_id    uuid NOT NULL REFERENCES songs (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('lyrics', 'chords')),
    data       jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (song_id, kind)
);
