package web

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/glgaspar/minusOne/db"
)

// deviceID identifies the browser tab, which player.js keeps in sessionStorage
// and sends with every request. The player belongs to a device, not a user:
// the same account is often open on the TV and on a phone.
func deviceID(c echo.Context) string {
	id := c.Request().Header.Get("X-Device-Id")
	if len(id) > 64 {
		return ""
	}
	return id
}

type heartbeatRequest struct {
	// Claim takes the player if nobody holds it. Without it the device only watches.
	Claim  bool            `json:"claim"`
	Status db.PlayerStatus `json:"status"`
}

type heartbeatResponse struct {
	Owner     bool             `json:"owner"`
	Playing   bool             `json:"playing"`
	OwnerName *string          `json:"ownerName"`
	Status    db.PlayerStatus  `json:"status"`
	Resume    *db.PlayerStatus `json:"resume,omitempty"`
	Requester *string          `json:"requester,omitempty"`
	Requested bool             `json:"requested"`
	Song      *playerSong      `json:"song"`
}

// playerHeartbeat is player.js's poll: it renews the owner's lease (or takes
// a free player over when claiming) and answers with who is playing what, so
// a skip, a new song or a handoff from any device reaches every player.
func (s *Server) playerHeartbeat(c echo.Context) error {
	device := deviceID(c)
	if device == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing device id")
	}
	var req heartbeatRequest
	if err := c.Bind(&req); err != nil {
		return err
	}

	ctx := c.Request().Context()
	state, err := s.db.Heartbeat(ctx, device, currentUser(c).ID, req.Claim, req.Status)
	if errors.Is(err, db.ErrNotFound) {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid item id")
	}
	if err != nil {
		return err
	}
	song, err := s.currentSong(ctx)
	if err != nil {
		return err
	}

	out := heartbeatResponse{
		Owner:     state.IsOwner,
		Playing:   state.Playing,
		OwnerName: state.Owner,
		Status:    state.Status,
		Resume:    state.Resume,
		Requested: state.Requested,
		Song:      song,
	}
	if state.IsOwner {
		out.Requester = state.Requester
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) requestPlayer(c echo.Context) error {
	err := s.db.RequestOwnership(c.Request().Context(), deviceID(c), currentUser(c).ID)
	if errors.Is(err, db.ErrNotOwner) {
		return c.NoContent(http.StatusConflict) // nobody to ask: player.js starts playing instead
	}
	if err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) acceptRequest(c echo.Context) error {
	return s.answerRequest(c, true)
}

func (s *Server) denyRequest(c echo.Context) error {
	return s.answerRequest(c, false)
}

func (s *Server) answerRequest(c echo.Context, accept bool) error {
	err := s.db.AnswerRequest(c.Request().Context(), deviceID(c), accept)
	if errors.Is(err, db.ErrNoRequest) {
		return c.NoContent(http.StatusConflict) // expired, or the player changed hands
	}
	if err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) releasePlayer(c echo.Context) error {
	if err := s.db.ReleaseOwnership(c.Request().Context(), deviceID(c)); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
