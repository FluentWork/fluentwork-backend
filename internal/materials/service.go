package materials

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// Service creates materials and runs refine.
type Service struct {
	store  Store
	blocks BlockWriter
	llm    Completer
	logger *slog.Logger
	now    func() time.Time
	newID  func() string
}

// NewService constructs B21 materials service.
func NewService(store Store, blocks BlockWriter, llm Completer, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  store,
		blocks: blocks,
		llm:    llm,
		logger: logger.With("component", "materials"),
		now:    time.Now,
		newID:  uuid.NewString,
	}
}

// Create inserts a queued material. URL content is stubbed.
func (s *Service) Create(ctx context.Context, userID string, req CreateRequest) (CreateResponse, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return CreateResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = KindPaste
	}
	switch kind {
	case KindPaste, KindSentence, KindURL:
	default:
		return CreateResponse{}, apierr.InvalidArgument("kind must be paste, sentence, or url")
	}
	content := strings.TrimSpace(req.Content)
	if kind == KindURL {
		if content == "" {
			return CreateResponse{}, apierr.InvalidArgument("content is required")
		}
		content = URLPlaceholder
	}
	if content == "" {
		return CreateResponse{}, apierr.InvalidArgument("content is required")
	}
	if len(content) > MaxContentLen {
		return CreateResponse{}, apierr.InvalidArgument("content exceeds 5000 bytes")
	}
	now := s.now().UTC()
	m := Material{
		ID:           s.newID(),
		UserID:       userID,
		Kind:         kind,
		Content:      content,
		RefineStatus: StatusQueued,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.InsertMaterial(ctx, m); err != nil {
		return CreateResponse{}, err
	}
	id := m.ID
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		if err := s.Refine(bg, id); err != nil && s.logger != nil {
			s.logger.Warn("material refine", "material_id", id, "err", err)
		}
	}()
	return CreateResponse{MaterialID: m.ID, RefineStatus: StatusQueued}, nil
}

// Get returns a live material owned by userID.
func (s *Service) Get(ctx context.Context, userID, materialID string) (Material, error) {
	userID = strings.TrimSpace(userID)
	materialID = strings.TrimSpace(materialID)
	if userID == "" {
		return Material{}, apierr.Unauthenticated("missing authenticated user")
	}
	if materialID == "" {
		return Material{}, apierr.InvalidArgument("material_id is required")
	}
	m, err := s.store.GetMaterial(ctx, userID, materialID)
	if err != nil {
		if err == ErrNotFound {
			return Material{}, apierr.NotFound("material not found")
		}
		return Material{}, err
	}
	if m.UserID != userID {
		return Material{}, apierr.PermissionDenied("material not owned by caller")
	}
	if m.DeletedAt != nil {
		return Material{}, apierr.NotFound("material not found")
	}
	return m, nil
}
