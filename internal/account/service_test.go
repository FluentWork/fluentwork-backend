package account

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
)

type recordingReassigner struct {
	guestID  string
	targetID string
	calls    int
}

func (r *recordingReassigner) ReassignFromGuest(_ context.Context, guestUserID, targetUserID string) error {
	r.calls++
	r.guestID = guestUserID
	r.targetID = targetUserID
	return nil
}

func testConfig() config.Config {
	return config.Config{
		HTTPAddr:        ":8080",
		AppEnv:          "development",
		AuthJWTSecret:   config.DevJWTSecret,
		AccessTokenTTL:  2 * time.Hour,
		RefreshTokenTTL: 24 * time.Hour,
	}
}

func newTestService(t *testing.T, reassigner Reassigner) (*Service, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(store, reassigner, testConfig(), logger)
	svc.now = func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }
	return svc, store
}

func TestIssueGuestIsIdempotentByDeviceID(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	first, err := svc.IssueGuest(ctx, "device-iphone-1")
	if err != nil {
		t.Fatalf("IssueGuest() 1: %v", err)
	}
	second, err := svc.IssueGuest(ctx, "device-iphone-1")
	if err != nil {
		t.Fatalf("IssueGuest() 2: %v", err)
	}
	if first.UserID != second.UserID {
		t.Fatalf("user id changed: %s vs %s", first.UserID, second.UserID)
	}
	if !first.IsGuest || first.AccessToken == "" || first.RefreshToken == "" {
		t.Fatalf("unexpected first token response: %+v", first)
	}
	if first.AccessToken == second.AccessToken {
		t.Fatal("expected rotated access token on retry")
	}
}

func TestIssueGuestRejectsInvalidDeviceID(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	_, err := svc.IssueGuest(context.Background(), " ")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error = %v", err)
	}
}

func TestMergeMovesGuestIdentityOntoRegisteredUser(t *testing.T) {
	reassigner := &recordingReassigner{}
	svc, store := newTestService(t, reassigner)
	ctx := context.Background()

	guestTokens, err := svc.IssueGuest(ctx, "device-iphone-1")
	if err != nil {
		t.Fatalf("IssueGuest: %v", err)
	}

	registered := User{
		ID:        "registered-user-1",
		IsGuest:   false,
		Status:    UserStatusActive,
		CreatedAt: svc.now(),
		UpdatedAt: svc.now(),
	}
	if err := store.CreateUser(ctx, registered); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	result, err := svc.Merge(ctx, registered.ID, "device-iphone-1")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if result.AlreadyMerged || result.MergedFromUserID == nil || *result.MergedFromUserID != guestTokens.UserID {
		t.Fatalf("unexpected merge result: %+v", result)
	}
	if reassigner.calls != 1 || reassigner.guestID != guestTokens.UserID || reassigner.targetID != registered.ID {
		t.Fatalf("reassigner not called correctly: %+v", reassigner)
	}

	bound, err := store.GetActiveByDeviceID(ctx, "device-iphone-1")
	if err != nil {
		t.Fatalf("GetActiveByDeviceID: %v", err)
	}
	if bound.ID != registered.ID || bound.IsGuest {
		t.Fatalf("device still bound to %+v", bound)
	}

	guest, err := store.GetUser(ctx, guestTokens.UserID)
	if err != nil {
		t.Fatalf("GetUser guest: %v", err)
	}
	if guest.Status != UserStatusMerged || guest.MergedIntoUserID == nil || *guest.MergedIntoUserID != registered.ID {
		t.Fatalf("guest not archived: %+v", guest)
	}

	if _, err := svc.Authenticate(ctx, guestTokens.AccessToken); err == nil {
		t.Fatal("expected merged guest token to be rejected")
	}

	again, err := svc.Merge(ctx, registered.ID, "device-iphone-1")
	if err != nil {
		t.Fatalf("Merge retry: %v", err)
	}
	if !again.AlreadyMerged {
		t.Fatalf("expected idempotent merge, got %+v", again)
	}
	if reassigner.calls != 1 {
		t.Fatalf("reassigner called %d times", reassigner.calls)
	}
}

func TestMergeRejectsGuestActor(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()
	guest, err := svc.IssueGuest(ctx, "device-iphone-1")
	if err != nil {
		t.Fatalf("IssueGuest: %v", err)
	}
	_, err = svc.Merge(ctx, guest.UserID, "device-iphone-1")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "PERMISSION_DENIED" {
		t.Fatalf("error = %v", err)
	}
}

func TestMergeConflictsWhenDeviceBelongsToAnotherAccount(t *testing.T) {
	svc, store := newTestService(t, NopReassigner{})
	ctx := context.Background()
	owner := User{ID: "owner-1", IsGuest: false, Status: UserStatusActive, CreatedAt: svc.now(), UpdatedAt: svc.now()}
	other := User{ID: "other-1", IsGuest: false, Status: UserStatusActive, CreatedAt: svc.now(), UpdatedAt: svc.now()}
	if err := store.CreateUser(ctx, owner); err != nil {
		t.Fatalf("CreateUser owner: %v", err)
	}
	if err := store.CreateUser(ctx, other); err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}
	if _, err := svc.IssueGuest(ctx, "device-iphone-1"); err != nil {
		t.Fatalf("IssueGuest: %v", err)
	}
	if _, err := svc.Merge(ctx, owner.ID, "device-iphone-1"); err != nil {
		t.Fatalf("Merge owner: %v", err)
	}
	_, err := svc.Merge(ctx, other.ID, "device-iphone-1")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "CONFLICT" {
		t.Fatalf("error = %v", err)
	}
}

func TestIssueGuestAfterMergeReturnsRegisteredIdentity(t *testing.T) {
	svc, store := newTestService(t, NopReassigner{})
	ctx := context.Background()
	if _, err := svc.IssueGuest(ctx, "device-iphone-1"); err != nil {
		t.Fatalf("IssueGuest: %v", err)
	}
	registered := User{ID: "registered-user-1", IsGuest: false, Status: UserStatusActive, CreatedAt: svc.now(), UpdatedAt: svc.now()}
	if err := store.CreateUser(ctx, registered); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := svc.Merge(ctx, registered.ID, "device-iphone-1"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	tokens, err := svc.IssueGuest(ctx, "device-iphone-1")
	if err != nil {
		t.Fatalf("IssueGuest after merge: %v", err)
	}
	if tokens.UserID != registered.ID || tokens.IsGuest {
		t.Fatalf("expected registered identity, got %+v", tokens)
	}
}

func TestMergeReplacesPreviousDeviceBinding(t *testing.T) {
	svc, store := newTestService(t, NopReassigner{})
	ctx := context.Background()
	registered := User{ID: "registered-user-1", IsGuest: false, Status: UserStatusActive, CreatedAt: svc.now(), UpdatedAt: svc.now()}
	if err := store.CreateUser(ctx, registered); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := svc.IssueGuest(ctx, "device-old"); err != nil {
		t.Fatalf("IssueGuest old: %v", err)
	}
	if _, err := svc.Merge(ctx, registered.ID, "device-old"); err != nil {
		t.Fatalf("Merge old: %v", err)
	}
	if _, err := svc.IssueGuest(ctx, "device-new"); err != nil {
		t.Fatalf("IssueGuest new: %v", err)
	}
	if _, err := svc.Merge(ctx, registered.ID, "device-new"); err != nil {
		t.Fatalf("Merge new: %v", err)
	}
	if _, err := store.GetActiveByDeviceID(ctx, "device-old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old device still bound: %v", err)
	}
	bound, err := store.GetActiveByDeviceID(ctx, "device-new")
	if err != nil {
		t.Fatalf("GetActiveByDeviceID new: %v", err)
	}
	if bound.ID != registered.ID {
		t.Fatalf("new device bound to %s", bound.ID)
	}
}
