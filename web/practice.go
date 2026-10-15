package web

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/glgaspar/minusOne/db"
)

type practicePage struct {
	page
	Song *playerSong
}

// practicePage plays one song on this device only, outside the shared queue,
// with tools for learning it.
func (s *Server) practicePage(c echo.Context) error {
	song, err := s.db.GetSong(c.Request().Context(), c.Param("id"))
	if errors.Is(err, db.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	if song.Status != "done" {
		return echo.ErrNotFound
	}
	title := "Practice"
	if song.Title != nil {
		title = *song.Title
	}
	return c.Render(http.StatusOK, "practice.html", practicePage{
		page: page{Title: title, User: currentUser(c)},
		Song: newPlayerSong(song),
	})
}
