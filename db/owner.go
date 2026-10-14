package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// OwnerLease is how long the owner keeps the player without polling.
	OwnerLease = 15 * time.Second
	// RequestTTL is how long a request to take over waits for the owner's answer.
	RequestTTL = 30 * time.Second
)

var (
	ErrNotOwner  = errors.New("device doesn't own the player")
	ErrNoRequest = errors.New("no pending request")
)

// PlayerStatus is what the owner's player reports with every poll.
type PlayerStatus struct {
	ItemID   *string `json:"itemId"`
	Paused   bool    `json:"paused"`
	Position float64 `json:"position"`
}

// PlayerState is the player's ownership as seen by one device.
type PlayerState struct {
	IsOwner bool         // this device holds the lease
	Playing bool         // some device holds the lease
	Owner   *string      // username of the owner, if Playing
	Status  PlayerStatus // the owner's last report
	// Resume is the last report before this device's heartbeat, for picking up
	// a song where the previous owner left it. Only set when IsOwner.
	Resume *PlayerStatus
	// Requester is the username asking to take over, while the request is pending.
	Requester *string
	// Requested reports whether this device has a pending request.
	Requested bool
}

// Heartbeat renews the device's lease and records its status, or takes the
// player over if nobody holds it. Without claim it only reads the state, for
// devices watching the player without playing.
func (d *DB) Heartbeat(ctx context.Context, deviceID, userID string, claim bool, st PlayerStatus) (*PlayerState, error) {
	var resume *PlayerStatus
	if claim {
		var prev PlayerStatus
		err := d.pool.QueryRow(ctx, `
			WITH prev AS (SELECT item_id, paused, position FROM queue_owner FOR UPDATE)
			UPDATE queue_owner q
			SET device_id = $1, user_id = $2, lease_until = now() + make_interval(secs => $3),
			    item_id = $4, paused = $5, position = $6,
			    -- taking over answers this device's own request
			    req_device_id = CASE WHEN q.req_device_id = $1 THEN NULL ELSE q.req_device_id END,
			    req_user_id   = CASE WHEN q.req_device_id = $1 THEN NULL ELSE q.req_user_id END,
			    req_at        = CASE WHEN q.req_device_id = $1 THEN NULL ELSE q.req_at END
			FROM prev
			WHERE q.device_id = $1 OR q.lease_until IS NULL OR q.lease_until < now()
			RETURNING prev.item_id, prev.paused, prev.position`,
			deviceID, userID, OwnerLease.Seconds(), st.ItemID, st.Paused, st.Position,
		).Scan(&prev.ItemID, &prev.Paused, &prev.Position)
		switch {
		case err == nil:
			resume = &prev
		case errors.Is(err, pgx.ErrNoRows): // someone else is playing
		case isInvalidUUID(err):
			return nil, ErrNotFound
		default:
			return nil, fmt.Errorf("renewing player lease: %w", err)
		}
	}

	state, err := d.playerState(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if state.IsOwner {
		state.Resume = resume
	}
	return state, nil
}

func (d *DB) playerState(ctx context.Context, deviceID string) (*PlayerState, error) {
	var s PlayerState
	err := d.pool.QueryRow(ctx, `
		SELECT coalesce(o.device_id = $1 AND o.lease_until > now(), false),
		       coalesce(o.lease_until > now(), false),
		       CASE WHEN o.lease_until > now() THEN u.username END,
		       o.item_id, o.paused, o.position,
		       CASE WHEN o.req_at > now() - make_interval(secs => $2) THEN r.username END,
		       coalesce(o.req_device_id = $1 AND o.req_at > now() - make_interval(secs => $2), false)
		FROM queue_owner o
		LEFT JOIN users u ON u.id = o.user_id
		LEFT JOIN users r ON r.id = o.req_user_id`,
		deviceID, RequestTTL.Seconds(),
	).Scan(&s.IsOwner, &s.Playing, &s.Owner,
		&s.Status.ItemID, &s.Status.Paused, &s.Status.Position,
		&s.Requester, &s.Requested)
	if err != nil {
		return nil, fmt.Errorf("reading player state: %w", err)
	}
	return &s, nil
}

// RequestOwnership asks the owner to pass the player to this device,
// replacing any other pending request. It returns ErrNotOwner if nobody is
// playing (the device can just start the player) or if it already owns it.
func (d *DB) RequestOwnership(ctx context.Context, deviceID, userID string) error {
	tag, err := d.pool.Exec(ctx, `
		UPDATE queue_owner
		SET req_device_id = $1, req_user_id = $2, req_at = now()
		WHERE lease_until > now() AND device_id <> $1`, deviceID, userID)
	if err != nil {
		return fmt.Errorf("requesting player: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotOwner
	}
	return nil
}

// AnswerRequest lets the owner accept or deny the pending request. Accepting
// hands the lease to the requester, who resumes from the owner's last report.
func (d *DB) AnswerRequest(ctx context.Context, deviceID string, accept bool) error {
	tag, err := d.pool.Exec(ctx, `
		UPDATE queue_owner
		SET device_id   = CASE WHEN $3 THEN req_device_id ELSE device_id END,
		    user_id     = CASE WHEN $3 THEN req_user_id ELSE user_id END,
		    lease_until = CASE WHEN $3 THEN now() + make_interval(secs => $4) ELSE lease_until END,
		    req_device_id = NULL, req_user_id = NULL, req_at = NULL
		WHERE device_id = $1 AND lease_until > now()
		  AND req_device_id IS NOT NULL AND req_at > now() - make_interval(secs => $2)`,
		deviceID, RequestTTL.Seconds(), accept, OwnerLease.Seconds())
	if err != nil {
		return fmt.Errorf("answering player request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoRequest
	}
	return nil
}

// ReleaseOwnership gives the player up, so the next device to start it takes over.
func (d *DB) ReleaseOwnership(ctx context.Context, deviceID string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE queue_owner SET lease_until = NULL WHERE device_id = $1`, deviceID)
	if err != nil {
		return fmt.Errorf("releasing player: %w", err)
	}
	return nil
}
