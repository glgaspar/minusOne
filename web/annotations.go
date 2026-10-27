package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/glgaspar/minusOne/db"
	"github.com/glgaspar/minusOne/processor"
)

// songAnnotation answers a song's annotation of a kind as JSON (db.Lyrics,
// db.Chords), or 404 while it has none. The players fetch it once per song.
func (s *Server) songAnnotation(kind string) echo.HandlerFunc {
	return func(c echo.Context) error {
		var data json.RawMessage
		err := s.db.GetAnnotation(c.Request().Context(), c.Param("id"), kind, &data)
		if errors.Is(err, db.ErrNotFound) {
			return echo.ErrNotFound
		}
		if err != nil {
			return err
		}
		return c.JSONBlob(http.StatusOK, data)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// lyricsCandidates lists the LRCLIB entries someone can pick for a song: for
// the words in ?q=, or the song's own search terms.
func (s *Server) lyricsCandidates(c echo.Context) error {
	ctx := c.Request().Context()
	song, err := s.db.GetSong(ctx, c.Param("id"))
	if errors.Is(err, db.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	title, author := deref(song.Title), deref(song.Author)
	candidates, err := processor.LyricsCandidates(ctx, title, author, c.QueryParam("q"))
	if err != nil {
		c.Logger().Errorf("searching lyrics: %v", err)
		return echo.NewHTTPError(http.StatusBadGateway, "LRCLIB can't be reached right now")
	}
	var current db.Lyrics
	_ = s.db.GetAnnotation(ctx, song.ID, db.LyricsKind, &current)
	return c.JSON(http.StatusOK, map[string]any{
		"terms":      processor.LyricsSearchTerms(title, author),
		"current":    current.LrclibID,
		"candidates": candidates,
	})
}

// chooseLyrics sets a song's lyrics to an LRCLIB entry ({"lrclibId": ...}),
// or to be written down from the vocals ({"transcribe": true}), and queues
// the learning pass to time them.
func (s *Server) chooseLyrics(c echo.Context) error {
	ctx := c.Request().Context()
	var req struct {
		LrclibID   int64 `json:"lrclibId"`
		Transcribe bool  `json:"transcribe"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	song, err := s.db.GetSong(ctx, c.Param("id"))
	if errors.Is(err, db.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	// a pass running now might have read the old lyrics already
	if song.LearningStatus == "processing" {
		return echo.NewHTTPError(http.StatusConflict, "the song is being prepared for practice right now: try again in a few minutes")
	}

	// no lines: the learning pass transcribes them (see processor.timeLyrics)
	lyrics := &db.Lyrics{Source: "transcribed"}
	if !req.Transcribe {
		lyrics, err = processor.ChosenLyrics(ctx, req.LrclibID)
		if errors.Is(err, processor.ErrUntimedLyrics) {
			return echo.NewHTTPError(http.StatusBadRequest, "these lyrics aren't timed: pick others")
		}
		if err != nil {
			c.Logger().Errorf("fetching lyrics %d: %v", req.LrclibID, err)
			return echo.NewHTTPError(http.StatusBadGateway, "LRCLIB can't be reached right now")
		}
	}
	if err := s.db.PutAnnotation(ctx, song.ID, db.LyricsKind, lyrics); err != nil {
		return err
	}
	if err := s.db.QueueLearning(ctx, song.ID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, lyrics)
}
