package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

type DB struct {
	pool *pgxpool.Pool
}

type Song struct {
	ID          string  `json:"id"`
	SourceURL   string  `json:"url"`
	Title       *string `json:"title"`
	Author      *string `json:"author"`
	OriginalKey *string `json:"-"`
	Extension   *string `json:"extension"`
	Status      string  `json:"status"`
	Attempts    int     `json:"attempts"`
	LastError   *string `json:"lastError,omitempty"`
	AddedBy     *string `json:"addedBy,omitempty"` // username
	// the learning pass's queue state (see ClaimLearning)
	LearningStatus string    `json:"learningStatus"`
	LearningError  *string   `json:"learningError,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	Tracks         []Track   `json:"tracks"`
}

type Track struct {
	ID         string `json:"id"`
	SongID     string `json:"songId"`
	Type       string `json:"type"`
	StorageKey string `json:"-"`
	Extension  string `json:"extension"`
	SizeBytes  *int64 `json:"sizeBytes,omitempty"`
}

var (
	ErrNoJob      = errors.New("no pending job")
	ErrSongExists = errors.New("song already in catalog")
	ErrSongBusy   = errors.New("song is being processed")
)

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func Connect(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close() { d.pool.Close() }

func (d *DB) Migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := d.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("running migration %s: %w", name, err)
		}
	}
	return nil
}

func (d *DB) CreateSong(ctx context.Context, sourceURL, addedBy string) (string, error) {
	var id string

	query := `
	INSERT INTO songs (source_url, added_by) 
	VALUES ($1, $2) RETURNING id`

	err := d.pool.QueryRow(ctx, query, sourceURL, addedBy).Scan(&id)

	if isUniqueViolation(err, "songs_source_url_idx") {
		return "", ErrSongExists
	}

	if err != nil {
		return "", fmt.Errorf("inserting song: %w", err)
	}

	return id, nil
}

const songColumns = `
	s.id, s.source_url, s.title, s.author, s.original_key, s.extension,
	s.status, s.attempts, s.last_error, u.username, s.created_at,
	s.learning_status, s.learning_error`

func scanSong(row pgx.Row) (*Song, error) {
	s := &Song{Tracks: []Track{}}

	err := row.Scan(
		&s.ID,
		&s.SourceURL,
		&s.Title,
		&s.Author,
		&s.OriginalKey,
		&s.Extension,
		&s.Status,
		&s.Attempts,
		&s.LastError,
		&s.AddedBy,
		&s.CreatedAt,
		&s.LearningStatus,
		&s.LearningError)

	return s, err
}

func (d *DB) ListSongs(ctx context.Context, query string) ([]*Song, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT`+songColumns+`
		FROM songs s
		LEFT JOIN users u ON u.id = s.added_by
		WHERE $1 = ''
			OR s.title  ILIKE '%' || $1 || '%'
			OR s.author ILIKE '%' || $1 || '%'
		ORDER BY s.created_at DESC
		LIMIT 200`, query)

	if err != nil {
		return nil, fmt.Errorf("listing songs: %w", err)
	}
	defer rows.Close()

	songs := []*Song{}

	for rows.Next() {
		s, err := scanSong(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning song: %w", err)
		}

		songs = append(songs, s)
	}

	return songs, rows.Err()
}

func (d *DB) ClaimNext(ctx context.Context, staleAfter time.Duration) (*Song, error) {
	row := d.pool.QueryRow(ctx, `
		UPDATE songs
		SET status = 'processing', attempts = attempts + 1, locked_at = now(), updated_at = now()
		WHERE id = (
			SELECT id FROM songs
			WHERE status = 'pending'
			   OR (status = 'processing' AND locked_at < now() - make_interval(secs => $1))
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, source_url, attempts, original_key, extension, title, author`, staleAfter.Seconds())

	s := &Song{Status: "processing"}
	if err := row.Scan(&s.ID, &s.SourceURL, &s.Attempts, &s.OriginalKey, &s.Extension, &s.Title, &s.Author); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoJob
		}
		return nil, fmt.Errorf("claiming job: %w", err)
	}
	return s, nil
}

func (d *DB) SetOriginal(ctx context.Context, songID, title, author, key, ext string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE songs
		SET title = $2, author = $3, original_key = $4, extension = $5, updated_at = now()
		WHERE id = $1`, songID, title, author, key, ext)
	if err != nil {
		return fmt.Errorf("updating song %s: %w", songID, err)
	}
	return nil
}

func (d *DB) AddTrack(ctx context.Context, t Track) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO tracks (song_id, type, storage_key, extension, size_bytes)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (song_id, type)
		DO UPDATE SET storage_key = EXCLUDED.storage_key,
		              extension   = EXCLUDED.extension,
		              size_bytes  = EXCLUDED.size_bytes`,
		t.SongID, t.Type, t.StorageKey, t.Extension, t.SizeBytes)
	if err != nil {
		return fmt.Errorf("inserting track %s for song %s: %w", t.Type, t.SongID, err)
	}
	return nil
}

// MarkDone makes a song Ready and queues its learning pass.
func (d *DB) MarkDone(ctx context.Context, songID string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE songs
		SET status = 'done', locked_at = NULL, last_error = NULL,
		    learning_status = 'pending', learning_attempts = 0, learning_error = NULL, updated_at = now()
		WHERE id = $1`, songID)
	return err
}

func (d *DB) MarkFailed(ctx context.Context, songID string, jobErr error, maxAttempts int) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE songs
		SET status = CASE WHEN attempts >= $3 THEN 'failed' ELSE 'pending' END,
		    last_error = $2, locked_at = NULL, updated_at = now()
		WHERE id = $1`, songID, jobErr.Error(), maxAttempts)
	return err
}

// Requeue gives a claimed song back without counting the attempt,
// for when the worker shuts down in the middle of a job.
func (d *DB) Requeue(ctx context.Context, songID string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE songs
		SET status = 'pending', attempts = greatest(attempts - 1, 0), locked_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'processing'`, songID)
	return err
}

func (d *DB) GetSong(ctx context.Context, id string) (*Song, error) {
	s, err := scanSong(d.pool.QueryRow(ctx, `
		SELECT`+songColumns+`
		FROM songs s
		LEFT JOIN users u ON u.id = s.added_by
		WHERE s.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting song %s: %w", id, err)
	}

	rows, err := d.pool.Query(ctx, `
		SELECT id, song_id, type, storage_key, extension, size_bytes
		FROM tracks WHERE song_id = $1 ORDER BY type`, id)
	if err != nil {
		return nil, fmt.Errorf("getting tracks for %s: %w", id, err)
	}
	tracks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Track])
	if err != nil {
		return nil, fmt.Errorf("scanning tracks for %s: %w", id, err)
	}
	s.Tracks = tracks
	return s, nil
}

// isInvalidUUID reports whether err comes from passing a malformed id, which
// callers treat the same as an id that doesn't exist.
func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

// DeleteSong removes a song and its tracks. Songs being processed, either
// pass, can't be deleted (ErrSongBusy): the worker would keep uploading files
// for a song that no longer exists. The caller removes the files from storage.
func (d *DB) DeleteSong(ctx context.Context, id string) error {
	tag, err := d.pool.Exec(ctx, `
		DELETE FROM songs WHERE id = $1 AND status <> 'processing' AND learning_status <> 'processing'`, id)
	if isInvalidUUID(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("deleting song %s: %w", id, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM songs WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("checking song %s: %w", id, err)
	}
	if exists {
		return ErrSongBusy
	}
	return ErrNotFound
}

// RetrySong puts a failed song, or its failed learning pass, back in the queue
// with a fresh set of attempts. It returns ErrNotFound if the song doesn't
// exist or neither failed.
func (d *DB) RetrySong(ctx context.Context, id string) error {
	tag, err := d.pool.Exec(ctx, `
		UPDATE songs
		SET status = CASE WHEN status = 'failed' THEN 'pending' ELSE status END,
		    attempts = CASE WHEN status = 'failed' THEN 0 ELSE attempts END,
		    last_error = CASE WHEN status = 'failed' THEN NULL ELSE last_error END,
		    learning_status = CASE WHEN learning_status = 'failed' THEN 'pending' ELSE learning_status END,
		    learning_attempts = CASE WHEN learning_status = 'failed' THEN 0 ELSE learning_attempts END,
		    learning_error = CASE WHEN learning_status = 'failed' THEN NULL ELSE learning_error END,
		    locked_at = NULL, updated_at = now()
		WHERE id = $1 AND (status = 'failed' OR learning_status = 'failed')`, id)
	if isInvalidUUID(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("retrying song %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
