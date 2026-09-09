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
	UserStatusActive  = "active"
	UserStatusMerged  = "merged"
	UserStatusDeleted = "deleted"
)

const (
	// ConfirmationDeleteMyData is the A4 confirmation phrase for DELETE /account/data.
	ConfirmationDeleteMyData = "DELETE-MY-DATA"
	// UndeleteWindow is the support restore window after A4 delete.
	UndeleteWindow = 30 * 24 * time.Hour
	// ExportReadyDelay is the stub SLA returned by POST /account/export.
	ExportReadyDelay = 24 * time.Hour
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
	DeletedAt        *time.Time
	TombstoneAt      *time.Time
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

// DeleteDataRequest is DELETE /account/data.
type DeleteDataRequest struct {
	ConfirmationCode string `json:"confirmation_code"`
}

// DeleteDataResult is returned by A4 delete (48_ §1.1.6).
type DeleteDataResult struct {
	Cascaded       map[string]int `json:"cascaded"`
	BackupPurgeAt  string         `json:"backup_purge_at"`
	AlreadyDeleted bool           `json:"already_deleted"`
}

// ExportDataRequest is POST /account/export.
type ExportDataRequest struct {
	EmailTo string `json:"email_to"`
}

// ExportDataResult is the async export enqueue stub.
type ExportDataResult struct {
	ExportID         string `json:"export_id"`
	EmailTo          string `json:"email_to"`
	EstimatedReadyAt string `json:"estimated_ready_at"`
}

// UndeleteUserRequest is POST /internal/v1/support/undelete-user.
type UndeleteUserRequest struct {
	UserID string `json:"user_id"`
	Reason string `json:"reason"`
	Actor  string `json:"actor"`
}

// UndeleteUserResult is returned after a successful support restore.
type UndeleteUserResult struct {
	Restored map[string]int `json:"restored"`
	UserID   string         `json:"user_id"`
}

// Tombstone is one A4 tombstones row.
type Tombstone struct {
	ID         string
	UserID     string
	EntityType string
	EntityID   string
	DeletedAt  time.Time
	PurgeAt    time.Time
	CreatedAt  time.Time
}

// AuditLog is one support audit_logs row.
type AuditLog struct {
	ID        string
	Actor     string
	Action    string
	UserID    string
	Reason    string
	CreatedAt time.Time
}

// ExportJob is an in-process export enqueue stub (email send is V1.5).
type ExportJob struct {
	ID               string
	UserID           string
	EmailTo          string
	EstimatedReadyAt time.Time
	CreatedAt        time.Time
}
