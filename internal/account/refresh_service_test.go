package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

func TestRefreshRotatesTheCredentialAndKeepsTheIdentity(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	guest, err := svc.IssueGuest(ctx, "device-refresh-rotate")
	if err != nil {
		t.Fatalf("IssueGuest: %v", err)
	}
	refreshed, err := svc.Refresh(ctx, guest.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.UserID != guest.UserID {
		t.Fatalf("user changed: %s vs %s", refreshed.UserID, guest.UserID)
	}
	if !refreshed.IsGuest || refreshed.AccessToken == "" || refreshed.TokenType != tokenTypeBearer {
		t.Fatalf("unexpected refresh response: %+v", refreshed)
	}
	if refreshed.RefreshToken == guest.RefreshToken {
		t.Fatal("expected a rotated refresh token")
	}
}

func TestRefreshRejectsTheConsumedCredential(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	guest, err := svc.IssueGuest(ctx, "device-refresh-replay")
	if err != nil {
		t.Fatalf("IssueGuest: %v", err)
	}
	if _, err := svc.Refresh(ctx, guest.RefreshToken); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	_, err = svc.Refresh(ctx, guest.RefreshToken)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "UNAUTHENTICATED" {
		t.Fatalf("replayed refresh error = %v", err)
	}
}

func TestRefreshRejectsAnUnknownCredential(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	if _, err := svc.IssueGuest(ctx, "device-refresh-unknown"); err != nil {
		t.Fatalf("IssueGuest: %v", err)
	}
	_, err := svc.Refresh(ctx, "not-a-real-refresh-token")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "UNAUTHENTICATED" {
		t.Fatalf("error = %v", err)
	}
}

func TestRefreshRejectsAnEmptyCredential(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	_, err := svc.Refresh(context.Background(), "   ")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error = %v", err)
	}
}

func TestRefreshRejectsAnExpiredCredential(t *testing.T) {
	svc, _ := newTestService(t, NopReassigner{})
	ctx := context.Background()

	guest, err := svc.IssueGuest(ctx, "device-refresh-expired")
	if err != nil {
		t.Fatalf("IssueGuest: %v", err)
	}
	svc.now = func() time.Time {
		return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC).Add(25 * time.Hour)
	}
	_, err = svc.Refresh(ctx, guest.RefreshToken)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "UNAUTHENTICATED" {
		t.Fatalf("error = %v", err)
	}
}

func TestRefreshRejectsADeletedAccount(t *testing.T) {
	svc, store := newTestService(t, NopReassigner{})
	ctx := context.Background()

	guest, err := svc.IssueGuest(ctx, "device-refresh-deleted")
	if err != nil {
		t.Fatalf("IssueGuest: %v", err)
	}
	now := svc.now()
	if err := store.MarkDeleted(ctx, guest.UserID, now, now); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	_, err = svc.Refresh(ctx, guest.RefreshToken)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "UNAUTHENTICATED" {
		t.Fatalf("error = %v", err)
	}
}
