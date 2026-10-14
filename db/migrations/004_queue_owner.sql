-- The device playing the queue. There is one row. The owner's player renews
-- lease_until with every poll and reports what it's doing; once the lease
-- runs out nobody is playing, and the next device to start the player takes over.
CREATE TABLE IF NOT EXISTS queue_owner (
    id            boolean PRIMARY KEY DEFAULT true CHECK (id),
    device_id     text,
    user_id       uuid REFERENCES users (id) ON DELETE SET NULL,
    lease_until   timestamptz,
    -- the owner's last report, shown to the others and resumed on a takeover
    item_id       uuid,
    paused        boolean NOT NULL DEFAULT false,
    position      double precision NOT NULL DEFAULT 0,
    -- a device asking the owner to pass the player on
    req_device_id text,
    req_user_id   uuid REFERENCES users (id) ON DELETE SET NULL,
    req_at        timestamptz
);

INSERT INTO queue_owner DEFAULT VALUES ON CONFLICT DO NOTHING;
