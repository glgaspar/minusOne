package web

import (
	"errors"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/glgaspar/minusOne/db"
	"github.com/glgaspar/minusOne/processor"
)

// originalTrack is the type the player uses for the downloaded audio; it isn't
// a row in tracks but is listed with them so it can be mixed like any other.
const originalTrack = "original"

var trackLabels = map[string]string{
	originalTrack: "Original",
	"vocals":      "Vocals",
	"drums":       "Drums",
	"bass":        "Bass",
	"guitar":      "Guitar",
	"piano":       "Piano",
	"other":       "Other instruments",
}

// trackOrder puts the original first, then the voice, then the instruments.
var trackOrder = map[string]int{originalTrack: 0, "vocals": 1, "drums": 2, "bass": 3, "guitar": 4, "piano": 5, "other": 6}

func trackLabel(trackType string) string {
	if label, ok := trackLabels[trackType]; ok {
		return label
	}
	return strings.ToUpper(trackType[:1]) + trackType[1:]
}

type playerPage struct {
	page
	Queue []db.QueueItem
}

// playerSong is what player.js needs to play the song at the head of the queue.
type playerSong struct {
	ItemID  string        `json:"itemId"`
	SongID  string        `json:"songId"`
	Title   string        `json:"title"`
	Author  string        `json:"author"`
	VideoID string        `json:"videoId"`
	Tracks  []playerTrack `json:"tracks"`
}

type playerTrack struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	URL   string `json:"url"`
	// Muted is the starting state when the user has no saved preference:
	// once a song has split tracks, the original would double them.
	Muted bool `json:"muted"`
}

func (s *Server) playerPage(c echo.Context) error {
	queue, err := s.db.ListQueue(c.Request().Context())
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "player.html", playerPage{
		page:  page{Title: "Player", User: currentUser(c)},
		Queue: queue,
	})
}

// playerCurrent returns the song at the head of the queue as JSON, or 204 if
// the queue is empty. player.js polls it, so a skip or a new song from another
// device reaches the player on its own.
func (s *Server) playerCurrent(c echo.Context) error {
	ctx := c.Request().Context()
	queue, err := s.db.ListQueue(ctx)
	if err != nil {
		return err
	}
	if len(queue) == 0 {
		return c.NoContent(http.StatusNoContent)
	}
	head := queue[0]

	song, err := s.db.GetSong(ctx, head.Song.ID)
	if err != nil {
		return err
	}

	out := playerSong{
		ItemID:  head.ID,
		SongID:  song.ID,
		VideoID: processor.VideoID(song.SourceURL),
	}
	if song.Title != nil {
		out.Title = *song.Title
	}
	if song.Author != nil {
		out.Author = *song.Author
	}

	hasStems := len(song.Tracks) > 0
	if song.OriginalKey != nil {
		out.Tracks = append(out.Tracks, playerTrack{Type: originalTrack, Muted: hasStems})
	}
	for _, t := range song.Tracks {
		out.Tracks = append(out.Tracks, playerTrack{Type: t.Type})
	}
	sort.SliceStable(out.Tracks, func(i, j int) bool {
		oi, iKnown := trackOrder[out.Tracks[i].Type]
		oj, jKnown := trackOrder[out.Tracks[j].Type]
		if iKnown != jKnown {
			return iKnown
		}
		if oi != oj {
			return oi < oj
		}
		return out.Tracks[i].Type < out.Tracks[j].Type
	})
	for i := range out.Tracks {
		t := &out.Tracks[i]
		t.Label = trackLabel(t.Type)
		t.URL = "/songs/" + song.ID + "/audio/" + t.Type
	}

	return c.JSON(http.StatusOK, out)
}

// songAudio streams one track of a song from storage. Going through the app
// keeps the bucket private and lets the browser use range requests.
func (s *Server) songAudio(c echo.Context) error {
	ctx := c.Request().Context()
	song, err := s.db.GetSong(ctx, c.Param("id"))
	if errors.Is(err, db.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}

	var key string
	if trackType := c.Param("track"); trackType == originalTrack {
		if song.OriginalKey != nil {
			key = *song.OriginalKey
		}
	} else {
		for _, t := range song.Tracks {
			if t.Type == trackType {
				key = t.StorageKey
			}
		}
	}
	if key == "" {
		return echo.ErrNotFound
	}

	obj, modTime, err := s.store.Open(ctx, key)
	if errors.Is(err, processor.ErrObjectNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	defer obj.Close()

	w := c.Response()
	w.Header().Set("Content-Type", processor.AudioContentType(path.Ext(key)))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, c.Request(), path.Base(key), modTime, obj)
	return nil
}

func (s *Server) queueList(c echo.Context) error {
	queue, err := s.db.ListQueue(c.Request().Context())
	if err != nil {
		return err
	}
	return c.Render(http.StatusOK, "queue-list", queue)
}

func (s *Server) addToQueue(c echo.Context) error {
	n, err := s.db.AddToQueue(c.Request().Context(), c.FormValue("song_id"), currentUser(c).ID)
	switch {
	case errors.Is(err, db.ErrSongNotReady), errors.Is(err, db.ErrNotFound):
		toast(c, "Only songs that are ready can be queued.", true)
	case err != nil:
		return err
	case n == 1:
		toast(c, "Added to the queue. It's up now.", false)
	default:
		toast(c, "Added to the queue (#"+strconv.Itoa(n)+").", false)
	}
	return c.NoContent(http.StatusOK)
}

// removeFromQueue is used for skipping, finishing and removing songs. It answers
// with the updated list for htmx; player.js ignores the body.
func (s *Server) removeFromQueue(c echo.Context) error {
	if err := s.db.RemoveFromQueue(c.Request().Context(), c.Param("id")); err != nil {
		return err
	}
	return s.queueList(c)
}
