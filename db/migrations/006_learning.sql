-- The learning pass splits a Ready song into instruments for practicing. It
-- runs only while no song waits for its karaoke pass, so it has its own queue
-- state: none -> pending (when the song is Ready) -> processing -> done | failed
ALTER TABLE songs ADD COLUMN IF NOT EXISTS learning_status text NOT NULL DEFAULT 'none'
    CHECK (learning_status IN ('none', 'pending', 'processing', 'done', 'failed'));
ALTER TABLE songs ADD COLUMN IF NOT EXISTS learning_attempts int NOT NULL DEFAULT 0;
ALTER TABLE songs ADD COLUMN IF NOT EXISTS learning_error text;
ALTER TABLE songs ADD COLUMN IF NOT EXISTS learning_locked_at timestamptz;

CREATE INDEX IF NOT EXISTS songs_learning_claimable_idx
    ON songs (created_at)
    WHERE learning_status IN ('pending', 'processing');

-- Songs Ready before this get the pass too. Those from before any splitting
-- have only their original; the pass makes their karaoke tracks first.
-- (A Ready song is otherwise always past 'none', so this only matches them.)
UPDATE songs SET learning_status = 'pending' WHERE status = 'done' AND learning_status = 'none';
