package processor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"sync/atomic"
	"time"

	"github.com/glgaspar/minusOne/db"
)

const (
	pollInterval = 2 * time.Second // how often to check the queue when it's empty
	yieldCheck   = 5 * time.Second // how often a learning pass checks for songs waiting for karaoke
	staleAfter   = time.Hour       // a job locked longer than this is assumed dead and retried; above passTimeout
	maxAttempts  = 3
)

// Run processes queued songs one at a time until ctx is cancelled: their
// karaoke pass first, and their learning pass when no song waits for one.
// It only returns an error when the database stops working.
func Run(ctx context.Context, database *db.DB, store *Store, models Models) error {
	for {
		job, err := database.ClaimNext(ctx, staleAfter)
		if errors.Is(err, db.ErrNoJob) {
			job, err = database.ClaimLearning(ctx, staleAfter)
			if err == nil {
				if err := runLearning(ctx, database, store, models, job); err != nil {
					return err
				}
				continue
			}
		}
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

		song := newSong(job)
		if err := finish(ctx, database, song, song.Process(ctx, database, store, models), job.Attempts); err != nil {
			return err
		}
	}
}

func newSong(job *db.Song) *Song {
	song := &Song{ID: job.ID, URL: job.SourceURL}
	if job.OriginalKey != nil && job.Extension != nil {
		song.OriginalKey, song.Extension = *job.OriginalKey, *job.Extension
	}
	if job.Title != nil {
		song.Title = *job.Title
	}
	if job.Author != nil {
		song.Author = *job.Author
	}
	return song
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
	if err := s.splitAndSave(ctx, karaokePass(models), database, store); err != nil {
		return err
	}
	s.lookUpLyrics(ctx, database)
	return nil
}

// lookUpLyrics finds the song's lyrics, unless it has them. They're a bonus:
// not finding them, or LRCLIB being down, doesn't fail the song, and the
// learning pass tries again.
func (s *Song) lookUpLyrics(ctx context.Context, database *db.DB) {
	var lyrics db.Lyrics
	if err := database.GetAnnotation(ctx, s.ID, db.LyricsKind, &lyrics); err == nil {
		return
	}
	if _, err := s.decodeOriginal(ctx); err != nil {
		log.Printf("looking up lyrics for %s: %v", s.ID, err)
		return
	}
	found, err := s.findLyrics(ctx, database)
	switch {
	case err != nil:
		log.Printf("looking up lyrics for %s: %v", s.ID, err)
	case !found:
		log.Printf("no lyrics found for %s (%s)", s.ID, s.Title)
	}
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

// ProcessLearning splits a Ready song into instruments. A song from before
// tracks were split gets its karaoke tracks first.
func (s *Song) ProcessLearning(ctx context.Context, database *db.DB, store *Store, models Models) error {
	workDir, err := os.MkdirTemp("", "minusone-"+s.ID+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	s.WorkDir = workDir

	if err := s.fetchOriginal(ctx, store); err != nil {
		return err
	}
	stored, err := database.GetSong(ctx, s.ID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(stored.Tracks, func(t db.Track) bool { return t.Type == "vocals" }) {
		if err := s.splitAndSave(ctx, karaokePass(models), database, store); err != nil {
			return err
		}
		s.Tracks = nil
	}
	s.lookUpLyrics(ctx, database)
	return s.splitAndSave(ctx, learningPass(models), database, store)
}

// runLearning runs a learning pass, and gives it back to the queue if a song
// comes in for its karaoke pass meanwhile: that one shouldn't wait minutes
// for instruments nobody asked for yet.
func runLearning(ctx context.Context, database *db.DB, store *Store, models Models, job *db.Song) error {
	passCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var yielded atomic.Bool
	go func() {
		tick := time.NewTicker(yieldCheck)
		defer tick.Stop()
		for {
			select {
			case <-passCtx.Done():
				return
			case <-tick.C:
				if waiting, err := database.SongsWaiting(passCtx); err == nil && waiting {
					yielded.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	song := newSong(job)
	jobErr := song.ProcessLearning(passCtx, database, store, models)
	switch {
	case jobErr == nil:
		log.Printf("split the instruments of %s (%s)", song.ID, song.Title)
		return database.MarkLearningDone(ctx, song.ID)

	case yielded.Load() || ctx.Err() != nil:
		log.Printf("set aside the instruments of %s, requeueing", song.ID)
		return database.RequeueLearning(context.WithoutCancel(ctx), song.ID)

	default:
		log.Printf("splitting the instruments of %s failed (attempt %d/%d): %v", song.ID, job.Attempts, maxAttempts, jobErr)
		return database.MarkLearningFailed(ctx, song.ID, jobErr, maxAttempts)
	}
}
