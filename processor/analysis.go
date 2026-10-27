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

// minTranscribedLines: a transcription shorter than this is noise Whisper
// heard in an instrumental song.
const minTranscribedLines = 3

// timeLyrics times the song's lyrics word by word against its vocals. With no
// lyrics, or lyrics that don't fit the vocals (another song's), it writes them
// down from the vocals with Whisper first. Lyrics already timed, or an
// instrumental, are left as they are.
func (s *Song) timeLyrics(ctx context.Context, database *db.DB, store *Store) error {
	var lyrics db.Lyrics
	err := database.GetAnnotation(ctx, s.ID, db.LyricsKind, &lyrics)
	found := err == nil
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	if found && (lyrics.WordSynced || lyrics.Instrumental) {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, passTimeout)
	defer cancel()
	vocals, err := s.trackAudio(ctx, database, store, "vocals")
	if err != nil {
		return err
	}
	if found && len(lyrics.Lines) > 0 {
		lines, score, err := s.align(ctx, vocals, lyrics.Lines)
		if err != nil {
			return err
		}
		if score >= minAlignScore {
			lyrics.Lines, lyrics.WordSynced = lines, true
			return database.PutAnnotation(ctx, s.ID, db.LyricsKind, lyrics)
		}
		log.Printf("the lyrics found for %s don't fit its vocals (score %.2f): transcribing them instead", s.ID, score)
	}

	out, err := output(ctx, "nice", "-n", "10", "minusone-analyze", "transcribe", vocals)
	if err != nil {
		return fmt.Errorf("transcribing the lyrics: %w", err)
	}
	transcribed := db.Lyrics{Source: "transcribed"}
	if err := json.Unmarshal(out, &transcribed); err != nil {
		return fmt.Errorf("transcribing the lyrics: reading the result: %w", err)
	}
	if len(transcribed.Lines) < minTranscribedLines {
		transcribed.Lines, transcribed.Instrumental = nil, true
		return database.PutAnnotation(ctx, s.ID, db.LyricsKind, transcribed)
	}
	// the transcription is the vocals' own words: no need to check the score
	if transcribed.Lines, _, err = s.align(ctx, vocals, transcribed.Lines); err != nil {
		return err
	}
	transcribed.WordSynced = true
	return database.PutAnnotation(ctx, s.ID, db.LyricsKind, transcribed)
}

// align times lines word by word against the vocals, and says how well they
// fit (see minAlignScore).
func (s *Song) align(ctx context.Context, vocals string, lines []db.LyricLine) ([]db.LyricLine, float64, error) {
	texts := make([]map[string]string, len(lines))
	for i, l := range lines {
		texts[i] = map[string]string{"text": l.Text}
	}
	linesPath := filepath.Join(s.WorkDir, "lines.json")
	if err := writeJSON(linesPath, texts); err != nil {
		return nil, 0, err
	}
	out, err := output(ctx, "nice", "-n", "10", "minusone-analyze", "align", vocals, linesPath)
	if err != nil {
		return nil, 0, fmt.Errorf("timing the lyrics: %w", err)
	}
	var aligned struct {
		Score float64        `json:"score"`
		Lines []db.LyricLine `json:"lines"`
	}
	if err := json.Unmarshal(out, &aligned); err != nil {
		return nil, 0, fmt.Errorf("timing the lyrics: reading the result: %w", err)
	}
	return aligned.Lines, aligned.Score, nil
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
