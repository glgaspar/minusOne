package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/glgaspar/minusOne/db"
)

type Song struct {
	ID          string
	URL         string
	Title       string
	Author      string
	Extension   string
	OriginalKey string

	// WorkDir is this job's scratch directory. The original and the split
	// tracks are written here, uploaded, and the directory is removed at the end.
	WorkDir string
	Tracks  []Track
}

type Track struct {
	Type      string // vocals, instrumental, drums, bass, guitar, piano, other
	Extension string
	Path      string // local file in WorkDir, written by split
	Key       string // object key in storage, set by UploadTracks
	Size      int64
}

func (s *Song) OriginalPath() string {
	return filepath.Join(s.WorkDir, "original"+s.Extension)
}

// Download fetches the best audio stream into WorkDir with yt-dlp and uploads
// it to storage as the original.
//
// yt-dlp is used instead of a Go library because YouTube keeps changing what
// stream URLs need (signatures, throttling params, tokens) and yt-dlp is the
// one that keeps up. It must be on PATH, along with deno for YouTube's JS challenges.
func (s *Song) Download(ctx context.Context, store *Store) error {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--no-playlist",
		"--no-warnings",
		"--format", "bestaudio",
		"--output", filepath.Join(s.WorkDir, "original.%(ext)s"),
		// one JSON line once the file is in place; artist falls back to the channel name
		"--print", "after_move:[%(title)j, %(artist,channel,uploader)j, %(filepath)j]",
		s.URL,
	)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := lastLine(stderr.String()); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("running yt-dlp: %w", err)
	}

	var info [3]string // title, artist, file path
	if err := json.Unmarshal([]byte(lastLine(stdout.String())), &info); err != nil {
		return fmt.Errorf("reading yt-dlp output %q: %w", stdout.String(), err)
	}
	s.Title, s.Author = info[0], info[1]
	s.Extension = filepath.Ext(info[2])

	file, err := os.Open(info[2])
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}

	s.OriginalKey = OriginalKey(s.ID, s.Extension)
	return store.Put(ctx, s.OriginalKey, file, stat.Size(), AudioContentType(s.Extension))
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// AudioContentType covers what yt-dlp's bestaudio returns; mime.TypeByExtension
// depends on the system's mime database and calls .webm video.
func AudioContentType(ext string) string {
	switch ext {
	case ".webm":
		return "audio/webm"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".opus", ".ogg":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	}
	return "application/octet-stream"
}

// UploadTracks puts every split track into storage.
func (s *Song) UploadTracks(ctx context.Context, store *Store) error {
	for i := range s.Tracks {
		t := &s.Tracks[i]
		if err := uploadTrack(ctx, store, s.ID, t); err != nil {
			return err
		}
	}
	return nil
}

func uploadTrack(ctx context.Context, store *Store, songID string, t *Track) error {
	file, err := os.Open(t.Path)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	t.Size = info.Size()
	t.Key = TrackKey(songID, t.Type, t.Extension)
	return store.Put(ctx, t.Key, file, t.Size, AudioContentType(t.Extension))
}

// SaveToDatabase records the uploaded tracks.
func (s *Song) SaveToDatabase(ctx context.Context, database *db.DB) error {
	for _, t := range s.Tracks {
		size := t.Size
		err := database.AddTrack(ctx, db.Track{
			SongID:     s.ID,
			Type:       t.Type,
			StorageKey: t.Key,
			Extension:  t.Extension,
			SizeBytes:  &size,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
