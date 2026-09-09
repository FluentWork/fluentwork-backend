package sessionhistory

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

func encodeCursor(c Cursor) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

func decodeCursor(s string) (*Cursor, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty cursor")
	}
	raw, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	if c.ID == "" || c.StartedAt.IsZero() {
		return nil, fmt.Errorf("invalid cursor")
	}
	return &c, nil
}
