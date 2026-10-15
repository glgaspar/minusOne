package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Marker is a section of a song saved for practicing, in seconds.
type Marker struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// ListMarkers returns the user's markers for a song, in song order.
func (d *DB) ListMarkers(ctx context.Context, songID, userID string) ([]Marker, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT id, label, start_s, end_s FROM song_markers
		WHERE song_id = $1 AND user_id = $2
		ORDER BY start_s, end_s`, songID, userID)
	if isInvalidUUID(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("listing markers of %s: %w", songID, err)
	}
	markers, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Marker])
	if isInvalidUUID(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning markers of %s: %w", songID, err)
	}
	return markers, nil
}

// CreateMarker saves a marker and returns it with its id. It returns
// ErrNotFound if the song doesn't exist.
func (d *DB) CreateMarker(ctx context.Context, songID, userID string, m Marker) (Marker, error) {
	err := d.pool.QueryRow(ctx, `
		INSERT INTO song_markers (song_id, user_id, label, start_s, end_s)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		songID, userID, m.Label, m.Start, m.End).Scan(&m.ID)
	if isInvalidUUID(err) || isForeignKeyViolation(err) {
		return Marker{}, ErrNotFound
	}
	if err != nil {
		return Marker{}, fmt.Errorf("creating marker on %s: %w", songID, err)
	}
	return m, nil
}

// DeleteMarker deletes one of the user's markers; anyone else's is ErrNotFound.
func (d *DB) DeleteMarker(ctx context.Context, songID, markerID, userID string) error {
	tag, err := d.pool.Exec(ctx, `
		DELETE FROM song_markers WHERE id = $1 AND song_id = $2 AND user_id = $3`,
		markerID, songID, userID)
	if isInvalidUUID(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("deleting marker %s: %w", markerID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
