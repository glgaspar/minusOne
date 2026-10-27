package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The learning pass's queue, on Ready songs. Same shape as the song queue in
// db.go (ClaimNext, MarkDone, ...), on the learning_* columns.

// ClaimLearning claims the oldest Ready song waiting for its learning pass, or
// one whose pass was locked longer than staleAfter by a worker that died.
func (d *DB) ClaimLearning(ctx context.Context, staleAfter time.Duration) (*Song, error) {
	row := d.pool.QueryRow(ctx, `
		UPDATE songs
		SET learning_status = 'processing', learning_attempts = learning_attempts + 1,
		    learning_locked_at = now(), updated_at = now()
		WHERE id = (
			SELECT id FROM songs
			WHERE status = 'done' AND (learning_status = 'pending'
			   OR (learning_status = 'processing' AND learning_locked_at < now() - make_interval(secs => $1)))
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, source_url, learning_attempts, original_key, extension, title, author`, staleAfter.Seconds())

	s := &Song{}
	if err := row.Scan(&s.ID, &s.SourceURL, &s.Attempts, &s.OriginalKey, &s.Extension, &s.Title, &s.Author); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoJob
		}
		return nil, fmt.Errorf("claiming learning job: %w", err)
	}
	return s, nil
}

// SongsWaiting reports whether a song waits for its karaoke pass, which goes
// before any learning pass.
func (d *DB) SongsWaiting(ctx context.Context) (bool, error) {
	var waiting bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM songs WHERE status = 'pending')`).Scan(&waiting)
	return waiting, err
}

func (d *DB) MarkLearningDone(ctx context.Context, songID string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE songs SET learning_status = 'done', learning_locked_at = NULL, learning_error = NULL, updated_at = now()
		WHERE id = $1`, songID)
	return err
}

func (d *DB) MarkLearningFailed(ctx context.Context, songID string, jobErr error, maxAttempts int) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE songs
		SET learning_status = CASE WHEN learning_attempts >= $3 THEN 'failed' ELSE 'pending' END,
		    learning_error = $2, learning_locked_at = NULL, updated_at = now()
		WHERE id = $1`, songID, jobErr.Error(), maxAttempts)
	return err
}

// RequeueLearning gives a claimed learning pass back without counting the
// attempt: the worker shut down, or a song came in for its karaoke pass.
func (d *DB) RequeueLearning(ctx context.Context, songID string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE songs
		SET learning_status = 'pending', learning_attempts = greatest(learning_attempts - 1, 0),
		    learning_locked_at = NULL, updated_at = now()
		WHERE id = $1 AND learning_status = 'processing'`, songID)
	return err
}

// QueueLearning queues a Ready song's learning pass again: to time lyrics
// someone picked, say. Its finished steps are skipped. A pass running now is
// left alone: callers wait for it to end.
func (d *DB) QueueLearning(ctx context.Context, songID string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE songs
		SET learning_status = 'pending', learning_attempts = 0, learning_error = NULL, updated_at = now()
		WHERE id = $1 AND status = 'done' AND learning_status <> 'processing'`, songID)
	return err
}
