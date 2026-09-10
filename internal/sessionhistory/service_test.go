package sessionhistory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

type stubEval struct {
	summary *session.EvalSummary
}

func (s stubEval) Summary(context.Context, string) (*session.EvalSummary, error) {
	return s.summary, nil
}

func seedSession(t *testing.T, store *session.MemoryStore, userID, id string, at time.Time) {
	t.Helper()
	if err := store.CreateSession(context.Background(), session.Session{
		ID:        id,
		UserID:    userID,
		SceneType: "demo",
		Status:    session.StatusCreated,
		CreatedAt: at,
		UpdatedAt: at,
	}); err != nil {
		t.Fatalf("CreateSession %s: %v", id, err)
	}
}

func TestCursor_EncodeDecode(t *testing.T) {
	at := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	token, err := encodeCursor(Cursor{StartedAt: at, ID: "s-1"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeCursor(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "s-1" || !got.StartedAt.Equal(at) {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestCursor_InvalidInput(t *testing.T) {
	if _, err := decodeCursor("not-a-cursor"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := decodeCursor(""); err == nil {
		t.Fatal("expected empty error")
	}
}

func TestList_TwentyPlusCursor(t *testing.T) {
	store := session.NewMemoryStore()
	svc := NewService(store, nil, nil)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		seedSession(t, store, "user-1", fmt.Sprintf("s-%02d", i), now.Add(time.Duration(i)*time.Second))
	}
	page, err := svc.List(context.Background(), "user-1", "", 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 20 || page.NextCursor == nil || page.Size != 20 {
		t.Fatalf("page = %+v", page)
	}
	if page.Items[0].SessionID != "s-24" || page.Items[19].SessionID != "s-05" {
		t.Fatalf("first page ids = %s .. %s", page.Items[0].SessionID, page.Items[19].SessionID)
	}
}

func TestList_Pagination(t *testing.T) {
	store := session.NewMemoryStore()
	svc := NewService(store, nil, nil)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		seedSession(t, store, "user-1", fmt.Sprintf("s-%02d", i), now.Add(time.Duration(i)*time.Second))
	}
	first, err := svc.List(context.Background(), "user-1", "", 20)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.List(context.Background(), "user-1", *first.NextCursor, 20)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(second.Items) != 5 || second.NextCursor != nil {
		t.Fatalf("second = %+v", second)
	}
	if second.Items[0].SessionID != "s-04" || second.Items[4].SessionID != "s-00" {
		t.Fatalf("second ids = %s .. %s", second.Items[0].SessionID, second.Items[4].SessionID)
	}
}

func TestList_LastPageShort(t *testing.T) {
	TestList_Pagination(t)
}

func TestList_Empty(t *testing.T) {
	svc := NewService(session.NewMemoryStore(), nil, nil)
	page, err := svc.List(context.Background(), "user-1", "", 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("page = %+v", page)
	}
}

func TestList_TamperedCursor(t *testing.T) {
	svc := NewService(session.NewMemoryStore(), nil, nil)
	_, err := svc.List(context.Background(), "user-1", "%%%not-base64%%%", 20)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 400 {
		t.Fatalf("err = %v", err)
	}
}

func TestList_SizeOverMaxForcedDefault(t *testing.T) {
	store := session.NewMemoryStore()
	svc := NewService(store, nil, nil)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		seedSession(t, store, "user-1", fmt.Sprintf("s-%02d", i), now.Add(time.Duration(i)*time.Second))
	}
	page, err := svc.List(context.Background(), "user-1", "", 200)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Size != 20 || len(page.Items) != 20 {
		t.Fatalf("size=%d n=%d", page.Size, len(page.Items))
	}
}

func TestList_CursorStabilitySameTimestamp(t *testing.T) {
	store := session.NewMemoryStore()
	svc := NewService(store, nil, nil)
	at := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "b", "c"} {
		seedSession(t, store, "user-1", id, at)
	}
	first, err := svc.List(context.Background(), "user-1", "", 2)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].SessionID != "c" || first.Items[1].SessionID != "b" {
		t.Fatalf("first = %+v", first.Items)
	}
	second, err := svc.List(context.Background(), "user-1", *first.NextCursor, 2)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].SessionID != "a" {
		t.Fatalf("second = %+v", second.Items)
	}
}

func TestGetDetail_Full(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now().UTC()
	seedSession(t, store, "user-1", "s1", now)
	if _, _, _, err := store.EndSession(context.Background(), "s1", 12, []session.Utterance{
		{ID: "u1", SessionID: "s1", Seq: 1, Speaker: session.SpeakerUser, Text: "hello"},
		{ID: "u2", SessionID: "s1", Seq: 2, Speaker: session.SpeakerAI, Text: "hi"},
	}, now, nil); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := store.MarkSessionReviewed(context.Background(), "s1", []byte(`{}`), now); err != nil {
		t.Fatalf("MarkSessionReviewed: %v", err)
	}
	svc := NewService(store, stubEval{summary: &session.EvalSummary{
		Score: 0.8, Dims: session.EvalDims{Grammar: 0.7, Fluency: 0.8, Vocabulary: 0.9},
		Suggestions: []string{"try again"}, Complete: true,
	}}, nil)
	detail, err := svc.GetDetail(context.Background(), "user-1", "s1")
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.DurationSec != 12 || len(detail.Utterances) != 2 || detail.Review == nil || detail.Review.Score != 0.8 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Materials == nil {
		t.Fatal("materials should be empty slice")
	}
}

func TestGetDetail_CrossUserForbidden(t *testing.T) {
	store := session.NewMemoryStore()
	seedSession(t, store, "user-1", "s1", time.Now().UTC())
	svc := NewService(store, nil, nil)
	_, err := svc.GetDetail(context.Background(), "other", "s1")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 403 {
		t.Fatalf("err = %v", err)
	}
}

func TestGetDetail_SoftDeletedNotFound(t *testing.T) {
	store := session.NewMemoryStore()
	seedSession(t, store, "user-1", "s1", time.Now().UTC())
	if _, err := store.SoftDeleteForUser(context.Background(), "user-1", time.Now().UTC()); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	svc := NewService(store, nil, nil)
	_, err := svc.GetDetail(context.Background(), "user-1", "s1")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 404 {
		t.Fatalf("err = %v", err)
	}
}

func TestGetDetail_UndeleteRestores(t *testing.T) {
	store := session.NewMemoryStore()
	seedSession(t, store, "user-1", "s1", time.Now().UTC())
	if _, err := store.SoftDeleteForUser(context.Background(), "user-1", time.Now().UTC()); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if _, err := store.RestoreDeletedForUser(context.Background(), "user-1"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	svc := NewService(store, nil, nil)
	detail, err := svc.GetDetail(context.Background(), "user-1", "s1")
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.SessionID != "s1" {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestGetDetail_EmptyUtterances(t *testing.T) {
	store := session.NewMemoryStore()
	seedSession(t, store, "user-1", "s1", time.Now().UTC())
	svc := NewService(store, nil, nil)
	detail, err := svc.GetDetail(context.Background(), "user-1", "s1")
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.Utterances == nil || len(detail.Utterances) != 0 {
		t.Fatalf("utterances = %#v", detail.Utterances)
	}
}

func TestGetDetail_ReviewPendingNil(t *testing.T) {
	store := session.NewMemoryStore()
	seedSession(t, store, "user-1", "s1", time.Now().UTC())
	svc := NewService(store, stubEval{summary: &session.EvalSummary{Complete: false}}, nil)
	detail, err := svc.GetDetail(context.Background(), "user-1", "s1")
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.Review != nil {
		t.Fatalf("review = %+v", detail.Review)
	}
}

func TestGetDetail_ReviewFailed(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now().UTC()
	seedSession(t, store, "user-1", "s1", now)
	if err := store.EnqueueJob(context.Background(), session.Job{
		ID: "j-fail", SessionID: "s1", JobType: session.JobTypeSessionFinished,
		Status: session.JobStatusFailed, CreatedAt: now, UpdatedAt: now, AvailableAt: now,
	}); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	svc := NewService(store, nil, nil)
	detail, err := svc.GetDetail(context.Background(), "user-1", "s1")
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.Review == nil || detail.Review.Status != "failed" || detail.Review.Score != 0 {
		t.Fatalf("review = %+v", detail.Review)
	}
}

func TestGetDetail_ReviewReady(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now().UTC()
	seedSession(t, store, "user-1", "s1", now)
	if _, _, _, err := store.EndSession(context.Background(), "s1", 8, nil, now, nil); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := store.MarkSessionReviewed(context.Background(), "s1", []byte(`{}`), now); err != nil {
		t.Fatalf("reviewed: %v", err)
	}
	svc := NewService(store, stubEval{summary: &session.EvalSummary{
		Score: 0.5, Dims: session.EvalDims{Grammar: 0.4, Fluency: 0.5, Vocabulary: 0.6},
		Suggestions: []string{"shorten"}, Complete: true,
	}}, nil)
	detail, err := svc.GetDetail(context.Background(), "user-1", "s1")
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if detail.Review == nil || detail.Review.Score != 0.5 || detail.Review.Dims == nil {
		t.Fatalf("review = %+v", detail.Review)
	}
	if len(detail.Review.Suggestions) != 1 {
		t.Fatalf("suggestions = %v", detail.Review.Suggestions)
	}
}

func TestGetDetail_FiftyUtterancesP95(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Now().UTC()
	seedSession(t, store, "user-1", "s1", now)
	utts := make([]session.Utterance, 50)
	for i := 0; i < 50; i++ {
		utts[i] = session.Utterance{ID: fmt.Sprintf("u-%d", i), SessionID: "s1", Seq: i + 1, Speaker: session.SpeakerUser, Text: "ok"}
	}
	if _, _, _, err := store.EndSession(context.Background(), "s1", 60, utts, now, nil); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	svc := NewService(store, nil, nil)
	samples := make([]time.Duration, 20)
	for i := range samples {
		start := time.Now()
		if _, err := svc.GetDetail(context.Background(), "user-1", "s1"); err != nil {
			t.Fatalf("GetDetail: %v", err)
		}
		samples[i] = time.Since(start)
	}
	p95 := percentile(samples, 0.95)
	if p95 > 200*time.Millisecond {
		t.Fatalf("p95 = %s", p95)
	}
}

func percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), samples...)
	for i := 0; i < len(cp); i++ {
		for j := i + 1; j < len(cp); j++ {
			if cp[j] < cp[i] {
				cp[i], cp[j] = cp[j], cp[i]
			}
		}
	}
	idx := int(float64(len(cp)-1) * p)
	return cp[idx]
}
