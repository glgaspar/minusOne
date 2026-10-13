package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"

	"github.com/glgaspar/minusOne/db"
)

const (
	sessionCookie   = "minusone_session"
	sessionLifetime = 30 * 24 * time.Hour
	minPasswordLen  = 8
	maxPasswordLen  = 72 // bcrypt ignores anything past 72 bytes
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

// dummyHash is compared against when a username doesn't exist, so a failed
// login takes the same time either way and doesn't reveal which usernames exist.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not a real password"), bcrypt.DefaultCost)

// page holds what the layout needs on every page.
type page struct {
	Title string
	User  *db.User
}

type authPage struct {
	page
	Mode              string // "login" or "register"
	Username          string
	Error             string
	AllowRegistration bool
}

func currentUser(c echo.Context) *db.User {
	u, _ := c.Get("user").(*db.User)
	return u
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// loadUser resolves the session cookie into a user, if there is a valid one.
func (s *Server) loadUser(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if cookie, err := c.Cookie(sessionCookie); err == nil {
			u, err := s.db.GetSessionUser(c.Request().Context(), hashToken(cookie.Value))
			if err != nil && !errors.Is(err, db.ErrNotFound) {
				return err
			}
			if u != nil {
				c.Set("user", u)
			}
		}
		return next(c)
	}
}

func (s *Server) requireUser(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if currentUser(c) == nil {
			return redirect(c, "/login")
		}
		return next(c)
	}
}

func (s *Server) startSession(c echo.Context, userID string) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(sessionLifetime)

	if err := s.db.CreateSession(c.Request().Context(), hashToken(token), userID, expires); err != nil {
		return err
	}
	c.SetCookie(&http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		// Lax keeps the cookie off cross-site POSTs, which is what protects
		// the forms from CSRF.
		SameSite: http.SameSiteLaxMode,
		// Scheme() honours X-Forwarded-Proto, so this works behind a TLS-terminating proxy.
		Secure: c.Scheme() == "https",
	})
	return nil
}

func (s *Server) renderAuth(c echo.Context, p authPage) error {
	p.AllowRegistration = s.cfg.AllowRegistration
	if p.Mode == "register" {
		p.Title = "Create account"
	} else {
		p.Title = "Log in"
	}
	// htmx posts swap just the form, so a failed attempt keeps the page in place
	if isHTMX(c) {
		return c.Render(http.StatusOK, "auth-form", p)
	}
	return c.Render(http.StatusOK, "auth.html", p)
}

func (s *Server) loginPage(c echo.Context) error {
	if currentUser(c) != nil {
		return c.Redirect(http.StatusSeeOther, "/catalog")
	}
	return s.renderAuth(c, authPage{Mode: "login"})
}

func (s *Server) registerPage(c echo.Context) error {
	if currentUser(c) != nil {
		return c.Redirect(http.StatusSeeOther, "/catalog")
	}
	if !s.cfg.AllowRegistration {
		return c.Redirect(http.StatusSeeOther, "/login")
	}
	return s.renderAuth(c, authPage{Mode: "register"})
}

func (s *Server) login(c echo.Context) error {
	username := strings.TrimSpace(c.FormValue("username"))
	password := c.FormValue("password")
	ctx := c.Request().Context()

	u, err := s.db.GetUserByUsername(ctx, username)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	hash := dummyHash
	if u != nil {
		hash = []byte(u.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || u == nil {
		return s.renderAuth(c, authPage{Mode: "login", Username: username, Error: "Wrong username or password."})
	}

	if err := s.startSession(c, u.ID); err != nil {
		return err
	}
	return redirect(c, "/catalog")
}

func (s *Server) register(c echo.Context) error {
	if !s.cfg.AllowRegistration {
		return echo.NewHTTPError(http.StatusForbidden, "registration is closed")
	}
	username := strings.TrimSpace(c.FormValue("username"))
	password := c.FormValue("password")
	fail := func(msg string) error {
		return s.renderAuth(c, authPage{Mode: "register", Username: username, Error: msg})
	}

	switch {
	case !usernamePattern.MatchString(username):
		return fail("Username must be 3–32 characters: letters, numbers, dots, dashes or underscores.")
	case len(password) < minPasswordLen:
		return fail("Password must be at least 8 characters.")
	case len(password) > maxPasswordLen:
		return fail("Password must be at most 72 characters.")
	case password != c.FormValue("confirm"):
		return fail("Passwords don't match.")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u, err := s.db.CreateUser(c.Request().Context(), username, string(hash))
	if errors.Is(err, db.ErrUsernameTaken) {
		return fail("That username is taken.")
	}
	if err != nil {
		return err
	}

	if err := s.startSession(c, u.ID); err != nil {
		return err
	}
	return redirect(c, "/catalog")
}

func (s *Server) logout(c echo.Context) error {
	if cookie, err := c.Cookie(sessionCookie); err == nil {
		if err := s.db.DeleteSession(c.Request().Context(), hashToken(cookie.Value)); err != nil {
			return err
		}
	}
	c.SetCookie(&http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	return redirect(c, "/login")
}
