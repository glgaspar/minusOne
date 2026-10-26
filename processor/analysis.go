package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/glgaspar/minusOne/db"
)

// The analysis of a song beyond splitting it runs minusone-analyze
// (analysis/analyze.py), which, like audio-separator, must be on PATH.

// minAlignScore is the alignment score under which the lyrics are taken not
// to be the song's. Measured: a song's own lyrics score around -1 or above,
// another song's around -1.6.
const minAlignScore = -1.3

// alignLyrics times the song's lyrics word by word against its vocals, unless
// they are already, or it has none.
func (s *Song) alignLyrics(ctx context.Context, database *db.DB, store *Store) error {
	var lyrics db.Lyrics
	err := database.GetAnnotation(ctx, s.ID, db.LyricsKind, &lyrics)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil || lyrics.WordSynced || lyrics.Instrumental || len(lyrics.Lines) == 0 {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, passTimeout)
	defer cancel()
	vocals, err := s.trackAudio(ctx, database, store, "vocals")
	if err != nil {
		return err
	}
	texts := make([]map[string]string, len(lyrics.Lines))
	for i, l := range lyrics.Lines {
		texts[i] = map[string]string{"text": l.Text}
	}
	linesPath := filepath.Join(s.WorkDir, "lines.json")
	if err := writeJSON(linesPath, texts); err != nil {
		return err
	}
	out, err := output(ctx, "nice", "-n", "10", "minusone-analyze", "align", vocals, linesPath)
	if err != nil {
		return fmt.Errorf("timing the lyrics: %w", err)
	}
	var aligned struct {
		Score float64        `json:"score"`
		Lines []db.LyricLine `json:"lines"`
	}
	if err := json.Unmarshal(out, &aligned); err != nil {
		return fmt.Errorf("timing the lyrics: reading the result: %w", err)
	}
	if aligned.Score < minAlignScore {
		log.Printf("the lyrics found for %s don't fit its vocals (score %.2f); keeping their line timing", s.ID, aligned.Score)
		return nil
	}
	lyrics.Lines, lyrics.WordSynced = aligned.Lines, true
	return database.PutAnnotation(ctx, s.ID, db.LyricsKind, lyrics)
}

// trackAudio fetches one of the song's stored tracks into WorkDir and decodes
// it to 16 kHz mono, what the analysis models listen at.
func (s *Song) trackAudio(ctx context.Context, database *db.DB, store *Store, trackType string) (string, error) {
	stored, err := database.GetSong(ctx, s.ID)
	if err != nil {
		return "", err
	}
	key := ""
	for _, t := range stored.Tracks {
		if t.Type == trackType {
			key = t.StorageKey
		}
	}
	if key == "" {
		return "", fmt.Errorf("the song has no %s track", trackType)
	}
	obj, err := store.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("fetching the %s track: %w", trackType, err)
	}
	defer obj.Close()
	encoded := filepath.Join(s.WorkDir, trackType+"-stored"+filepath.Ext(key))
	file, err := os.Create(encoded)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(file, obj); err != nil {
		file.Close()
		return "", fmt.Errorf("fetching the %s track: %w", trackType, err)
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	decoded := filepath.Join(s.WorkDir, trackType+"-16k.wav")
	if err := run(ctx, "ffmpeg", "-v", "error", "-y", "-i", encoded, "-ar", "16000", "-ac", "1", decoded); err != nil {
		return "", fmt.Errorf("decoding the %s track: %w", trackType, err)
	}
	return decoded, nil
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
