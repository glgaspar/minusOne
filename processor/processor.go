package processor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/glgaspar/minusOne/db"
)

const (
	pollInterval = 2 * time.Second  // how often to check the queue when it's empty
	staleAfter   = 30 * time.Minute // a job locked longer than this is assumed dead and retried
	maxAttempts  = 3
)

// Run processes queued songs one at a time until ctx is cancelled.
// It only returns an error when the database stops working.
func Run(ctx context.Context, database *db.DB, store *Store) error {
	for {
		job, err := database.ClaimNext(ctx, staleAfter)
		if errors.Is(err, db.ErrNoJob) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(pollInterval):
				continue
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		song := &Song{ID: job.ID, URL: job.SourceURL}
		if err := finish(ctx, database, song, song.Process(ctx, database, store), job.Attempts); err != nil {
			return err
		}
	}
}

// Process runs the whole pipeline for one song.
func (s *Song) Process(ctx context.Context, database *db.DB, store *Store) error {
	workDir, err := os.MkdirTemp("", "minusone-"+s.ID+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	s.WorkDir = workDir

	if err := s.Download(ctx, store); err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	// saved right away so the catalog shows the title while the rest runs
	if err := database.SetOriginal(ctx, s.ID, s.Title, s.Author, s.OriginalKey, s.Extension); err != nil {
		return err
	}

	if err := s.SplitTracks(ctx); err != nil {
		return fmt.Errorf("splitting: %w", err)
	}
	if err := s.UploadTracks(ctx, store); err != nil {
		return fmt.Errorf("uploading tracks: %w", err)
	}
	return s.SaveToDatabase(ctx, database)
}

func finish(ctx context.Context, database *db.DB, song *Song, jobErr error, attempt int) error {
	switch {
	case jobErr == nil:
		log.Printf("processed %s (%s)", song.ID, song.Title)
		return database.MarkDone(ctx, song.ID)

	case ctx.Err() != nil:
		// shutting down mid-job: hand the song back so the next start picks it up
		log.Printf("interrupted %s, requeueing", song.ID)
		return database.Requeue(context.WithoutCancel(ctx), song.ID)

	default:
		log.Printf("processing %s failed (attempt %d/%d): %v", song.ID, attempt, maxAttempts, jobErr)
		return database.MarkFailed(ctx, song.ID, jobErr, maxAttempts)
	}
}
