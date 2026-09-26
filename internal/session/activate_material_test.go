package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

type stubMaterialSource struct {
	text            string
	err             error
	askedUserID     string
	askedMaterialID string
	calls           int
}

func (s *stubMaterialSource) MaterialText(_ context.Context, userID, materialID string) (string, error) {
	s.calls++
	s.askedUserID = userID
	s.askedMaterialID = materialID
	return s.text, s.err
}

func newMaterialActivationService(t *testing.T, src MaterialSource) (*Service, CreateResponse) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(store, config.Config{SessionTicketTTL: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if src != nil {
		svc.SetMaterialSource(src)
	}
	materialID := "m-1"
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup", MaterialID: &materialID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return svc, created
}

// The material's text is resolved from the session record, which is the only
// place that knows which material the session belongs to. The client's
// session.start frame carries a material_id it could simply make up.
func TestActivate_ResolvesTheMaterialTextFromTheSessionRecord(t *testing.T) {
	svc, created := newMaterialActivationService(t, &stubMaterialSource{text: "The deploy is blocked on the migration."})

	got, err := svc.Activate(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got.MaterialContext != "The deploy is blocked on the migration." {
		t.Fatalf("material context = %q", got.MaterialContext)
	}
}

// Whose material is asked for matters as much as whether one is: the session
// owner's, looked up by the id the session was created with.
func TestActivate_AsksForTheMaterialAsTheSessionOwner(t *testing.T) {
	src := &stubMaterialSource{text: "x"}
	svc, created := newMaterialActivationService(t, src)

	if _, err := svc.Activate(context.Background(), created.SessionID); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if src.calls != 1 {
		t.Fatalf("MaterialText called %d times, want 1", src.calls)
	}
	if src.askedUserID != "user-1" || src.askedMaterialID != "m-1" {
		t.Fatalf("asked (%q, %q), want (user-1, m-1)", src.askedUserID, src.askedMaterialID)
	}
}

func TestActivate_WithoutMaterialLeavesTheContextEmpty(t *testing.T) {
	src := &stubMaterialSource{text: "should never be read"}
	store := NewMemoryStore()
	svc := NewService(store, config.Config{SessionTicketTTL: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetMaterialSource(src)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.Activate(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got.MaterialContext != "" {
		t.Fatalf("material context = %q, want empty", got.MaterialContext)
	}
	if src.calls != 0 {
		t.Fatalf("MaterialText called %d times, want 0", src.calls)
	}
}

// A lookup that fails opens the session without context rather than failing the
// session: the learner would otherwise see "can't start", not "no material".
func TestActivate_MaterialLookupFailureDoesNotFailTheSession(t *testing.T) {
	svc, created := newMaterialActivationService(t, &stubMaterialSource{err: errors.New("material store down")})

	got, err := svc.Activate(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if got.MaterialContext != "" {
		t.Fatalf("material context = %q, want empty", got.MaterialContext)
	}
	if got.Status != StatusActive {
		t.Fatalf("status = %q, want active", got.Status)
	}
}

func TestActivate_CapsTheMaterialContextLength(t *testing.T) {
	long := strings.Repeat("字", materialContextMaxRunes+500)
	svc, created := newMaterialActivationService(t, &stubMaterialSource{text: long})

	got, err := svc.Activate(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if n := utf8.RuneCountInString(got.MaterialContext); n != materialContextMaxRunes {
		t.Fatalf("material context runes = %d, want %d", n, materialContextMaxRunes)
	}
}
