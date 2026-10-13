package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/kkdai/youtube/v2"

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
	Type      string // vocals, drums, bass, other...
	Extension string
	Path      string // local file in WorkDir, written by SplitTracks
	Key       string // object key in storage, set by UploadTracks
	Size      int64
}

func (s *Song) OriginalPath() string {
	return filepath.Join(s.WorkDir, "original"+s.Extension)
}

// Download fetches the audio into WorkDir and uploads it to storage as the original.
func (s *Song) Download(ctx context.Context, store *Store) error {
	client := youtube.Client{}

	video, err := client.GetVideoContext(ctx, s.URL)
	if err != nil {
		return fmt.Errorf("getting video metadata: %w", err)
	}
	s.Title = video.Title
	s.Author = video.Author

	audioFormats := video.Formats.WithAudioChannels().Type("audio")
	if len(audioFormats) == 0 {
		return errors.New("no audio-only stream found")
	}
	targetFormat := &audioFormats[0]

	stream, _, err := client.GetStreamContext(ctx, video, targetFormat)
	if err != nil {
		return fmt.Errorf("getting download stream: %w", err)
	}
	defer stream.Close()

	s.Extension = ".m4a"
	if strings.Contains(targetFormat.MimeType, "webm") {
		s.Extension = ".webm"
	}

	file, err := os.Create(s.OriginalPath())
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer file.Close()

	size, err := io.Copy(file, stream)
	if err != nil {
		return fmt.Errorf("saving audio to file: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	contentType, _, _ := strings.Cut(targetFormat.MimeType, ";")
	s.OriginalKey = OriginalKey(s.ID, s.Extension)
	return store.Put(ctx, s.OriginalKey, file, size, contentType)
}

// SplitTracks separates the original into one file per instrument in WorkDir
// and appends them to s.Tracks.
//
// TODO: run the separation (e.g. demucs on s.OriginalPath()). Until then a
// song finishes with only its original.
func (s *Song) SplitTracks(ctx context.Context) error {
	return nil
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
	return store.Put(ctx, t.Key, file, t.Size, mime.TypeByExtension(t.Extension))
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
