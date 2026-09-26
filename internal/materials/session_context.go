package materials

import "context"

// SessionMaterialSource adapts Service to session.MaterialSource.
type SessionMaterialSource struct {
	Service *Service
}

// MaterialText returns the material's text for a session's system prompt.
func (s SessionMaterialSource) MaterialText(ctx context.Context, userID, materialID string) (string, error) {
	if s.Service == nil {
		return "", nil
	}
	m, err := s.Service.Get(ctx, userID, materialID)
	if err != nil {
		return "", err
	}
	return m.Content, nil
}
