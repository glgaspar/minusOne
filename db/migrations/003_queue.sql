-- One shared play queue. The item with the lowest position is the one playing;
-- finishing or skipping a song deletes its item.
CREATE TABLE IF NOT EXISTS queue_items (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    position   bigserial NOT NULL,
    song_id    uuid NOT NULL REFERENCES songs (id) ON DELETE CASCADE,
    added_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS queue_items_position_idx ON queue_items (position);
