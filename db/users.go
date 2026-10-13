package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID           string
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

var (
	ErrUsernameTaken = errors.New("username already taken")
	ErrNotFound      = errors.New("not found")
)

func (d *DB) CreateUser(ctx context.Context, username, passwordHash string) (*User, error) {
	u := &User{Username: username, PasswordHash: passwordHash}
	err := d.pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash) VALUES ($1, $2)
		RETURNING id, created_at`, username, passwordHash,
	).Scan(&u.ID, &u.CreatedAt)
	if isUniqueViolation(err, "users_username_idx") {
		return nil, ErrUsernameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("inserting user: %w", err)
	}
	return u, nil
}

func (d *DB) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	u := &User{}
	err := d.pool.QueryRow(ctx, `
		SELECT id, username, password_hash, created_at
		FROM users WHERE lower(username) = lower($1)`, username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting user %s: %w", username, err)
	}
	return u, nil
}

// CreateSession stores a session for the hashed cookie token.
// Expired sessions of the same user are cleaned up on the way.
func (d *DB) CreateSession(ctx context.Context, tokenHash []byte, userID string, expiresAt time.Time) error {
	if _, err := d.pool.Exec(ctx,
		`DELETE FROM sessions WHERE user_id = $1 AND expires_at < now()`, userID,
	); err != nil {
		return fmt.Errorf("cleaning sessions: %w", err)
	}
	if _, err := d.pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt,
	); err != nil {
		return fmt.Errorf("inserting session: %w", err)
	}
	return nil
}

// GetSessionUser returns the user owning a non-expired session.
func (d *DB) GetSessionUser(ctx context.Context, tokenHash []byte) (*User, error) {
	u := &User{}
	err := d.pool.QueryRow(ctx, `
		SELECT u.id, u.username, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash,
	).Scan(&u.ID, &u.Username, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	return u, nil
}

func (d *DB) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}
