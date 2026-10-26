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
	pollInterval = 2 * time.Second // how often to check the queue when it's empty
	staleAfter   = time.Hour       // a job locked longer than this is assumed dead and retried; above passTimeout
	maxAttempts  = 3
)

// Run processes queued songs one at a time until ctx is cancelled.
// It only returns an error when the database stops working.
func Run(ctx context.Context, database *db.DB, store *Store, models Models) error {
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
		if job.OriginalKey != nil && job.Extension != nil {
			song.OriginalKey, song.Extension = *job.OriginalKey, *job.Extension
		}
		if job.Title != nil {
			song.Title = *job.Title
		}
		if err := finish(ctx, database, song, song.Process(ctx, database, store, models), job.Attempts); err != nil {
			return err
		}
	}
}

// Process gets a song ready to sing: it downloads it, unless the original is
// already stored (a retry, or a song from before tracks were split), and runs
// the karaoke pass.
func (s *Song) Process(ctx context.Context, database *db.DB, store *Store, models Models) error {
	workDir, err := os.MkdirTemp("", "minusone-"+s.ID+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	s.WorkDir = workDir

	if s.OriginalKey != "" {
		if err := s.fetchOriginal(ctx, store); err != nil {
			return err
		}
	} else {
		if err := s.Download(ctx, store); err != nil {
			return fmt.Errorf("downloading: %w", err)
		}
		// saved right away so the catalog shows the title while the rest runs
		if err := database.SetOriginal(ctx, s.ID, s.Title, s.Author, s.OriginalKey, s.Extension); err != nil {
			return err
		}
	}
	return s.splitAndSave(ctx, karaokePass(models), database, store)
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
