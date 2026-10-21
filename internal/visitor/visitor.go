// Package visitor identifies anonymous visitors and limits how much each one
// can add.
//
// There are no accounts. A visitor is a random id in a cookie: whoever holds
// the cookie sees and edits that visitor's listings. The id is a version 4
// UUID, 122 random bits, so it can't be guessed, and it is never shown in a
// URL or page.
package visitor

import (
	"context"
	"fmt"
	"net/http"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CookieName is the cookie that holds the visitor id.
const CookieName = "visitor"

// Retention is how long a visitor's data is kept after their last visit. The
// cookie lasts as long, and every visit renews both.
const Retention = 30 * 24 * time.Hour

// DB is what this package needs from a connection; pgx.Tx and
// *pgxpool.Pool both provide it.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// FromRequest returns the visitor id in r's cookie, if r has a well-formed
// one. The visitor may no longer exist; see Touch.
func FromRequest(r *http.Request) (uuid.UUID, bool) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(cookie.Value)
	if err != nil {
		return uuid.UUID{}, false
	}
	return id, true
}

// SetCookie gives the response a visitor cookie for id that lasts Retention.
//
// The cookie is HttpOnly, so page scripts can't read it, and SameSite=Lax, so
// other sites can't make the browser send it with a POST or DELETE: together
// with the API accepting only JSON bodies, that blocks cross-site request
// forgery. It is Secure whenever the request came over HTTPS, either directly
// or through the ingress, which terminates TLS and says so in
// X-Forwarded-Proto. Plain HTTP only happens locally.
func SetCookie(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	//nolint:gosec // Secure is set for every HTTPS request; see above.
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    id.String(),
		Path:     "/",
		MaxAge:   int(Retention / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

// Touch records a visit by id and reports whether that visitor exists. A
// visitor deleted for inactivity doesn't, even if their cookie survived.
func Touch(ctx context.Context, db DB, id uuid.UUID) (bool, error) {
	tag, err := db.Exec(ctx, "UPDATE visitors SET last_seen_at = now() WHERE id = $1", id)
	if err != nil {
		return false, fmt.Errorf("record visit: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Ensure returns id if that visitor exists, after recording the visit, and
// otherwise creates a new visitor. Visitors are only created when they first
// add something, so crawlers reading the page leave no rows behind.
func Ensure(ctx context.Context, db DB, id uuid.UUID, ok bool) (uuid.UUID, error) {
	if ok {
		exists, err := Touch(ctx, db, id)
		if err != nil || exists {
			return id, err
		}
	}
	// A new id is always generated here, never taken from the cookie, so a
	// visitor's id is always one the server chose at random.
	id = uuid.NewV4()
	if _, err := db.Exec(ctx, "INSERT INTO visitors (id) VALUES ($1)", id); err != nil {
		return uuid.UUID{}, fmt.Errorf("create visitor: %w", err)
	}
	return id, nil
}

// DeleteInactive deletes visitors not seen for Retention, with their listings
// and places, and returns how many it deleted.
func DeleteInactive(ctx context.Context, db DB) (int64, error) {
	tag, err := db.Exec(ctx, "DELETE FROM visitors WHERE last_seen_at < now() - make_interval(secs => $1)",
		Retention.Seconds())
	if err != nil {
		return 0, fmt.Errorf("delete inactive visitors: %w", err)
	}
	return tag.RowsAffected(), nil
}
