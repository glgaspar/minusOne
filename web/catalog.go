package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/glgaspar/minusOne/db"
	"github.com/glgaspar/minusOne/processor"
)

type catalogPage struct {
	page
	Query   string
	Songs   []*db.Song
	AddForm addForm
}

type addForm struct {
	URL   string
	Error string
}

type addSongResponse struct {
	Form addForm
	Song *db.Song
}

func (s *Server) catalogPage(c echo.Context) error {
	q := strings.TrimSpace(c.QueryParam("q"))
	songs, err := s.db.ListSongs(c.Request().Context(), q)
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "catalog.html", catalogPage{
		page:  page{Title: "Catalog", User: currentUser(c)},
		Query: q,
		Songs: songs,
	})
}

// catalogSongs re-renders only the list, for the search box.
func (s *Server) catalogSongs(c echo.Context) error {
	q := strings.TrimSpace(c.QueryParam("q"))
	songs, err := s.db.ListSongs(c.Request().Context(), q)
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "song-list", catalogPage{Query: q, Songs: songs})
}

// addSong answers with a fresh form (or the same form plus an error).
// On success it also sends the new row out-of-band into the list and
// triggers "songAdded", which closes the modal.
func (s *Server) addSong(c echo.Context) error {
	raw := c.FormValue("url")
	url, err := processor.CanonicalURL(raw)
	if errors.Is(err, processor.ErrNotYouTube) {
		return c.Render(http.StatusOK, "add-form", addForm{URL: raw, Error: "For now, only YouTube links are supported."})
	}
	if err != nil {
		return c.Render(http.StatusOK, "add-form", addForm{URL: raw, Error: "Couldn't find a video in that link."})
	}

	ctx := c.Request().Context()
	id, err := s.db.CreateSong(ctx, url, currentUser(c).ID)
	if errors.Is(err, db.ErrSongExists) {
		return c.Render(http.StatusOK, "add-form", addForm{URL: raw, Error: "That song is already in the catalog."})
	}
	if err != nil {
		return err
	}
	created, err := s.db.GetSong(ctx, id)
	if err != nil {
		return err
	}

	c.Response().Header().Set("HX-Trigger", "songAdded")
	return c.Render(http.StatusOK, "add-song-response", addSongResponse{Song: created})
}

// songRow is polled by rows that are still being processed; once the song is
// done the returned row has no polling attributes, so the polling stops.
func (s *Server) songRow(c echo.Context) error {
	sng, err := s.db.GetSong(c.Request().Context(), c.Param("id"))
	if errors.Is(err, db.ErrNotFound) {
		// 286 tells htmx to stop polling; the empty body removes the row
		return c.NoContent(286)
	}
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "song-row", sng)
}
