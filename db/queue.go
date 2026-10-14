package db

import (
	"context"
	"errors"
	"fmt"
)

type QueueItem struct {
	ID      string
	AddedBy *string // username
	Song    Song    // ID, SourceURL, Title, Author
}

var ErrSongNotReady = errors.New("song is not ready to play")

// AddToQueue appends a song to the end of the queue and returns how many
// songs are in the queue now, including the one playing.
// Only processed songs can be queued (ErrSongNotReady).
func (d *DB) AddToQueue(ctx context.Context, songID, userID string) (int, error) {
	tag, err := d.pool.Exec(ctx, `
		INSERT INTO queue_items (song_id, added_by)
		SELECT id, $2 FROM songs WHERE id = $1 AND status = 'done'`, songID, userID)
	if isInvalidUUID(err) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("queueing song %s: %w", songID, err)
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrSongNotReady
	}

	var count int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM queue_items`).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting queue: %w", err)
	}
	return count, nil
}

// ListQueue returns the queue in play order; the first item is the one playing.
func (d *DB) ListQueue(ctx context.Context) ([]QueueItem, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT q.id, u.username, s.id, s.source_url, s.title, s.author
		FROM queue_items q
		JOIN songs s ON s.id = q.song_id
		LEFT JOIN users u ON u.id = q.added_by
		ORDER BY q.position`)
	if err != nil {
		return nil, fmt.Errorf("listing queue: %w", err)
	}
	defer rows.Close()

	items := []QueueItem{}
	for rows.Next() {
		var it QueueItem
		if err := rows.Scan(&it.ID, &it.AddedBy, &it.Song.ID, &it.Song.SourceURL, &it.Song.Title, &it.Song.Author); err != nil {
			return nil, fmt.Errorf("scanning queue item: %w", err)
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// RemoveFromQueue deletes an item. Removing one that's already gone is not an
// error: two devices finishing or skipping the same song is expected.
// The song playing can only be removed by the device that owns the player, or
// by anyone while nobody is playing (ErrNotOwner).
func (d *DB) RemoveFromQueue(ctx context.Context, itemID, deviceID string) error {
	tag, err := d.pool.Exec(ctx, `
		DELETE FROM queue_items q
		WHERE q.id = $1 AND (
			q.position > (SELECT min(position) FROM queue_items)
			OR NOT EXISTS (
				SELECT 1 FROM queue_owner o
				WHERE o.lease_until > now() AND o.device_id IS DISTINCT FROM $2
			)
		)`, itemID, deviceID)
	if isInvalidUUID(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("removing queue item %s: %w", itemID, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM queue_items WHERE id = $1)`, itemID).Scan(&exists); err != nil {
		return fmt.Errorf("checking queue item %s: %w", itemID, err)
	}
	if exists {
		return ErrNotOwner
	}
	return nil
}
