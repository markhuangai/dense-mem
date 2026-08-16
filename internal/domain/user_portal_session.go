package domain

import (
	"time"

	"github.com/google/uuid"
)

// UserPortalSession is an opaque browser session for API-credential portal access.
// Only token fingerprints are persisted; the raw session and CSRF tokens stay
// in the browser and are returned once at creation time.
type UserPortalSession struct {
	SessionHash  string
	CredentialID uuid.UUID
	CSRFHash     string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}
