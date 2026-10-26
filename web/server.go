package web

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/glgaspar/minusOne/db"
	"github.com/glgaspar/minusOne/processor"
)

//go:embed templates static
var assets embed.FS

type Config struct {
	AllowRegistration bool
}

type Server struct {
	db    *db.DB
	store *processor.Store
	cfg   Config
	echo  *echo.Echo
}

func New(database *db.DB, store *processor.Store, cfg Config) *Server {
	s := &Server{db: database, store: store, cfg: cfg, echo: echo.New()}
	s.echo.HideBanner = true
	s.echo.Renderer = newRenderer()
	s.routes()
	return s
}

func (s *Server) routes() {
	e := s.echo
	e.Use(middleware.Recover())

	static, _ := fs.Sub(assets, "static")
	e.StaticFS("/static", static)

	e.Use(s.loadUser)

	e.GET("/", func(c echo.Context) error { return c.Redirect(http.StatusSeeOther, "/catalog") })
	e.GET("/login", s.loginPage)
	e.POST("/login", s.login)
	e.GET("/register", s.registerPage)
	e.POST("/register", s.register)
	e.POST("/logout", s.logout)

	app := e.Group("", s.requireUser)
	app.GET("/catalog", s.catalogPage)
	app.GET("/catalog/songs", s.catalogSongs)
	app.POST("/songs", s.addSong)
	app.GET("/songs/:id/row", s.songRow)
	app.POST("/songs/:id/retry", s.retrySong)
	app.DELETE("/songs/:id", s.deleteSong)
	app.GET("/songs/:id/audio/:track", s.songAudio)
	app.GET("/songs/:id/lyrics", s.songLyrics)

	app.GET("/practice/:id", s.practicePage)
	app.GET("/songs/:id/markers", s.listMarkers)
	app.POST("/songs/:id/markers", s.createMarker)
	app.DELETE("/songs/:id/markers/:markerId", s.deleteMarker)

	app.GET("/player", s.playerPage)
	app.POST("/player/heartbeat", s.playerHeartbeat)
	app.POST("/player/request", s.requestPlayer)
	app.POST("/player/request/accept", s.acceptRequest)
	app.POST("/player/request/deny", s.denyRequest)
	app.POST("/player/release", s.releasePlayer)
	app.GET("/queue", s.queueList)
	app.POST("/queue", s.addToQueue)
	app.DELETE("/queue/:id", s.removeFromQueue)
}

// Run serves on addr until ctx is cancelled, then gives in-flight requests
// a few seconds to finish.
func (s *Server) Run(ctx context.Context, addr string) error {
	errc := make(chan error, 1)
	go func() { errc <- s.echo.Start(addr) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.echo.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func isHTMX(c echo.Context) bool {
	return c.Request().Header.Get("HX-Request") == "true"
}

// redirect works for both plain form posts and htmx requests,
// which ignore 3xx responses and need HX-Redirect instead.
func redirect(c echo.Context, to string) error {
	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", to)
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, to)
}
