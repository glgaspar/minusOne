package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Lyrics are a song's lyrics, timed in seconds from the start of the song.
type Lyrics struct {
	Source       string      `json:"source"`             // "lrclib" or "transcribed"
	LrclibID     int64       `json:"lrclibId,omitempty"` // the LRCLIB entry they came from
	Language     string      `json:"language,omitempty"` // when transcribed, as Whisper detected it
	Instrumental bool        `json:"instrumental,omitempty"`
	WordSynced   bool        `json:"wordSynced"` // words are timed against the vocals, not just lines
	Lines        []LyricLine `json:"lines"`
}

type LyricLine struct {
	Start float64     `json:"start"`
	End   float64     `json:"end"`
	Text  string      `json:"text"`
	Words []LyricWord `json:"words,omitempty"`
}

type LyricWord struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Annotation kinds.
const (
	LyricsKind = "lyrics"
	ChordsKind = "chords"
)

// GetAnnotation reads a song's annotation of a kind into v. It returns
// ErrNotFound if the song has none.
func (d *DB) GetAnnotation(ctx context.Context, songID, kind string, v any) error {
	var data []byte
	err := d.pool.QueryRow(ctx, `SELECT data FROM song_annotations WHERE song_id = $1 AND kind = $2`, songID, kind).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("reading %s of %s: %w", kind, songID, err)
	}
	return json.Unmarshal(data, v)
}

// PutAnnotation saves (or replaces) a song's annotation of a kind.
func (d *DB) PutAnnotation(ctx context.Context, songID, kind string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `
		INSERT INTO song_annotations (song_id, kind, data) VALUES ($1, $2, $3)
		ON CONFLICT (song_id, kind) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		songID, kind, data)
	if err != nil {
		return fmt.Errorf("saving %s of %s: %w", kind, songID, err)
	}
	return nil
}
