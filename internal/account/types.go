// Package account implements guest identity issuance and guest-to-account merge.
//
// Endpoints follow fluentwork-meta backend technical design §3.2.5 and §4:
// POST /api/v1/auth/guest and POST /api/v1/account/merge.
package account

import (
	"time"
)

// User statuses from the account baseline in the backend technical design.
const (
	UserStatusActive = "active"
	UserStatusMerged = "merged"
)

// User is the account aggregate used by guest auth and merge.
type User struct {
	ID               string
	Email            *string
	Phone            *string
	DeviceID         *string
	IsGuest          bool
	PasswordHash     *string
	Status           string
	MergedIntoUserID *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RefreshToken is a persisted refresh credential.
type RefreshToken struct {
	ID        string
	UserID    string
	Hash      string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// TokenResponse is the guest/auth token envelope returned to clients.
type TokenResponse struct {
	UserID       string `json:"user_id"`
	IsGuest      bool   `json:"is_guest"`
	Status       string `json:"status"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

// MergeResponse is returned by POST /account/merge.
type MergeResponse struct {
	UserID           string  `json:"user_id"`
	IsGuest          bool    `json:"is_guest"`
	MergedFromUserID *string `json:"merged_from_user_id,omitempty"`
	AlreadyMerged    bool    `json:"already_merged"`
}

// GuestRequest is the body of POST /auth/guest.
type GuestRequest struct {
	DeviceID string `json:"device_id"`
}

// MergeRequest is the body of POST /account/merge.
type MergeRequest struct {
	DeviceID string `json:"device_id"`
}
