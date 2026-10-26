package web

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

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
		Song: newPlayerSong(song, true),
	})
}

const maxMarkerLabel = 40

func (s *Server) listMarkers(c echo.Context) error {
	markers, err := s.db.ListMarkers(c.Request().Context(), c.Param("id"), currentUser(c).ID)
	if errors.Is(err, db.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, markers)
}

func (s *Server) createMarker(c echo.Context) error {
	var m db.Marker
	if err := c.Bind(&m); err != nil {
		return err
	}
	m.Label = strings.TrimSpace(m.Label)
	switch {
	case m.Label == "" || utf8.RuneCountInString(m.Label) > maxMarkerLabel:
		return echo.NewHTTPError(http.StatusBadRequest, "the name must have 1 to 40 characters")
	case m.Start < 0 || m.End <= m.Start:
		return echo.NewHTTPError(http.StatusBadRequest, "the section must end after it starts")
	}

	m, err := s.db.CreateMarker(c.Request().Context(), c.Param("id"), currentUser(c).ID, m)
	if errors.Is(err, db.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, m)
}

func (s *Server) deleteMarker(c echo.Context) error {
	err := s.db.DeleteMarker(c.Request().Context(), c.Param("id"), c.Param("markerId"), currentUser(c).ID)
	if errors.Is(err, db.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
