package web

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/glgaspar/minusOne/db"
)

// songLyrics answers a song's timed lyrics as JSON (db.Lyrics), or 404 while
// it has none. The players fetch it once per song.
func (s *Server) songLyrics(c echo.Context) error {
	var lyrics db.Lyrics
	err := s.db.GetAnnotation(c.Request().Context(), c.Param("id"), db.LyricsKind, &lyrics)
	if errors.Is(err, db.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, lyrics)
}
